package clustersmoke

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"
)

// The throwaway objects the storage check applies.
//
// Fixed names rather than generated ones, so a run killed before cleanup is
// repaired by the next run rather than leaving a new orphan every time. The
// namespace is the cluster's own default: creating one would be a fourth thing
// to clean up, and the probe is deleted either way.
const (
	probeClaim     = "cluster-smoke"
	probePod       = "cluster-smoke"
	probeNamespace = "default"

	// probeSize is the smallest volume the hcloud CSI driver will provision.
	// Hetzner's minimum is 10 GiB; asking for less is rejected rather than
	// rounded up, which reads as a driver failure.
	probeSize = "10Gi"
)

// probeImage is the container the claim's consumer runs. It has to exist and
// exit-or-sleep without doing anything; the pod is scheduled purely so a
// WaitForFirstConsumer class has a consumer to bind against.
//
// Pinned by digest-bearing tag rather than :latest, which the policy pack
// refuses for exactly the reason it applies here: a probe that silently changes
// is a probe whose failure cannot be attributed.
const probeImage = "registry.k8s.io/pause:3.10"

// Options configures a run.
type Options struct {
	// Kubeconfig is the path to the kubeconfig file. Empty uses the standard
	// client-go loading rules, which respect KUBECONFIG.
	Kubeconfig string
	// Context is the kubeconfig context to use. Empty uses its current-context.
	Context string
	// StorageClass is the class the claim asks for. internal/pkg/platform owns the name.
	StorageClass string
	// BindTimeout bounds the wait for the claim to reach Bound.
	BindTimeout time.Duration
	// Logf receives progress. nil silences it.
	Logf func(string, ...any)
}

// DefaultBindTimeout is how long the storage check waits for Bound.
//
// Generous on purpose: the driver has to call the Hetzner API, create a volume
// and attach it to the node the pod landed on. Two minutes is comfortably more
// than the observed time on a healthy cluster and still short enough that a
// broken driver is reported rather than waited on.
const DefaultBindTimeout = 2 * time.Minute

// Runner executes the checks against a live cluster.
type Runner struct {
	client kubernetes.Interface
	// custom reads the operator's own resources. Nil when the caller has no
	// cluster with CRDs, which the check that uses it treats as "nothing to
	// look at".
	custom dynamic.Interface
	opts   Options
}

// New builds a Runner from a kubeconfig.
func New(opts Options) (*Runner, error) {
	if opts.BindTimeout <= 0 {
		opts.BindTimeout = DefaultBindTimeout
	}

	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}

	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if opts.Kubeconfig != "" {
		rules.ExplicitPath = opts.Kubeconfig
	}

	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules, &clientcmd.ConfigOverrides{CurrentContext: opts.Context}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("kubeconfig: %w", err)
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("kubernetes client: %w", err)
	}

	// A second client, for the one check that reads a custom resource. The
	// typed client knows nothing about CRDs, and generating a typed client for
	// one status field would tie this program to the operator's release cycle.
	custom, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("dynamic client: %w", err)
	}

	return &Runner{client: client, custom: custom, opts: opts}, nil
}

// NewWithClient builds a Runner around existing clients, for tests.
//
// A nil dynamic client is allowed and means "this cluster has no custom
// resources to read": the secret-store check skips rather than fails, which is
// what a test that is not about secret stores should get.
func NewWithClient(client kubernetes.Interface, custom dynamic.Interface, opts Options) *Runner {
	if opts.BindTimeout <= 0 {
		opts.BindTimeout = DefaultBindTimeout
	}

	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}

	return &Runner{client: client, custom: custom, opts: opts}
}

// Run executes every check and returns the whole report.
//
// It does not stop at the first failure. The three checks answer independent
// questions, and "the CSI is broken" should not hide "and so is the CCM" —
// finding both in one run is the difference between one investigation and two.
func (r *Runner) Run(ctx context.Context) Report {
	return Report{
		r.checkNodes(ctx),
		r.checkCrossNode(ctx),
		r.checkStorage(ctx),
		r.checkDataVolumes(ctx),
		r.checkSecretStores(ctx),
		r.checkNetworkPolicies(ctx),
		r.checkLoadBalancers(ctx),
		r.checkExternalMetrics(),
	}
}

// checkDataVolumes reads the claims in namespaces that say they hold data, and
// the reclaim policy of the class each one is bound to.
//
// Two lookups rather than one: a claim names a class, or names nothing and
// gets the default, and only the class knows what deleting the claim would do.
// Resolved here rather than in the judgement, which stays a pure function over
// what was found.
func (r *Runner) checkDataVolumes(ctx context.Context) Result {
	namespaces, err := r.client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: platform.DataNamespaceLabel,
	})
	if err != nil {
		return Result{Name: CheckDataVolumes, Status: StatusFailed,
			Detail: "listing namespaces: " + err.Error()}
	}

	reclaim, err := r.reclaimPolicies(ctx)
	if err != nil {
		return Result{Name: CheckDataVolumes, Status: StatusFailed, Detail: err.Error()}
	}

	var volumes []DataVolume

	for _, namespace := range namespaces.Items {
		claims, listErr := r.client.CoreV1().PersistentVolumeClaims(namespace.Name).
			List(ctx, metav1.ListOptions{})
		if listErr != nil {
			return Result{Name: CheckDataVolumes, Status: StatusFailed,
				Detail: "listing claims in " + namespace.Name + ": " + listErr.Error()}
		}

		for _, claim := range claims.Items {
			class := r.opts.StorageClass
			if claim.Spec.StorageClassName != nil && *claim.Spec.StorageClassName != "" {
				class = *claim.Spec.StorageClassName
			}

			volumes = append(volumes, DataVolume{
				Namespace: namespace.Name,
				Name:      claim.Name,
				Class:     class,
				Reclaim:   reclaim[class],
			})
		}
	}

	return DataVolumesAreRetained(volumes, len(namespaces.Items))
}

// reclaimPolicies maps every storage class to what deleting a claim on it
// does, and names the default class as itself as well.
//
// The default matters because a claim may name nothing: Kubernetes then binds
// it to whichever class carries the default annotation, which is the one this
// repository deliberately made `Delete`.
func (r *Runner) reclaimPolicies(ctx context.Context) (map[string]string, error) {
	classes, err := r.client.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing storage classes: %w", err)
	}

	policies := make(map[string]string, len(classes.Items))

	for _, class := range classes.Items {
		if class.ReclaimPolicy != nil {
			policies[class.Name] = string(*class.ReclaimPolicy)
		}
	}

	return policies, nil
}

// checkExternalMetrics asks discovery whether the aggregated external metrics
// group answers.
//
// Discovery rather than the APIService object, which would mean depending on
// kube-aggregator's client for one read. This asks the question the way every
// consumer of that API asks it: a group that discovery cannot reach is a group
// no HorizontalPodAutoscaler can read, whatever the object says about itself.
//
// No context: client-go's discovery interface predates them on this call.
func (r *Runner) checkExternalMetrics() Result {
	served, err := r.client.Discovery().ServerGroups()

	var failure string
	if err != nil {
		// Not returned as a failure of the check. A partial discovery failure
		// IS the answer — it names the groups that did not answer — and
		// ExternalMetricsServed decides whether ours is among them.
		failure = err.Error()
	}

	var groups []string

	if served != nil {
		for _, group := range served.Groups {
			groups = append(groups, group.Name)
		}
	}

	return ExternalMetricsServed(groups, failure)
}

func (r *Runner) checkNodes(ctx context.Context) Result {
	list, err := r.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return Result{Name: "every node is Ready", Status: StatusFailed,
			Detail: "listing nodes: " + err.Error()}
	}

	states := make([]NodeState, 0, len(list.Items))
	for _, node := range list.Items {
		states = append(states, NodeState{
			Name:                      node.Name,
			Ready:                     nodeReady(node),
			ExcludedFromLoadBalancers: excludedFromLoadBalancers(node),
		})
	}

	// The Result is the report, and the error is the same fact in a second
	// channel: every error a judgement returns comes through failed(), which
	// sets StatusFailed and the detail. Blanked rather than handled, and
	// TestJudgements_ReturnNoErrorWithoutAFailingStatus is what makes that
	// safe to keep doing.
	result, _ := NodesReady(states)

	return result
}

// excludedFromLoadBalancers reads the label that keeps a node out of every
// external load balancer's target list. Presence is what counts; the value is
// not read, which is upstream's own rule for it.
func excludedFromLoadBalancers(node corev1.Node) bool {
	_, found := node.Labels[LabelExcludeFromExternalLoadBalancers]

	return found
}

// nodeReady reads the Ready condition. A node with no Ready condition is not
// Ready: absence of the condition is absence of the guarantee.
func nodeReady(node corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}

	return false
}

func (r *Runner) checkLoadBalancers(ctx context.Context) Result {
	list, err := r.client.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return Result{Name: CheckExternalAddresses, Status: StatusFailed,
			Detail: "listing services: " + err.Error()}
	}

	var states []LoadBalancerState

	for _, service := range list.Items {
		if service.Spec.Type != corev1.ServiceTypeLoadBalancer {
			continue
		}

		states = append(states, LoadBalancerState{
			Namespace: service.Namespace,
			Name:      service.Name,
			Addresses: serviceAddresses(service),
		})
	}

	// The nodes decide whether an address means anything, so this check needs
	// them too. A listing error is reported rather than treated as "no nodes":
	// an empty list would make the eligibility question pass vacuously.
	nodes, err := r.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return Result{Name: CheckExternalAddresses,
			Status: StatusFailed, Detail: "listing nodes: " + err.Error()}
	}

	nodeStates := make([]NodeState, 0, len(nodes.Items))
	for _, node := range nodes.Items {
		nodeStates = append(nodeStates, NodeState{
			Name:                      node.Name,
			Ready:                     nodeReady(node),
			ExcludedFromLoadBalancers: excludedFromLoadBalancers(node),
		})
	}

	// The Result is the report, and the error is the same fact in a second
	// channel: every error a judgement returns comes through failed(), which
	// sets StatusFailed and the detail. Blanked rather than handled, and
	// TestJudgements_ReturnNoErrorWithoutAFailingStatus is what makes that
	// safe to keep doing.
	result, _ := ExternalAddresses(states, nodeStates)

	return result
}

// serviceAddresses collects whatever the cloud controller manager published —
// an IP or a hostname. Either counts: what is being checked is that the CCM
// answered, not which form it answered in.
func serviceAddresses(service corev1.Service) []string {
	var out []string

	for _, ingress := range service.Status.LoadBalancer.Ingress {
		switch {
		case ingress.IP != "":
			out = append(out, ingress.IP)
		case ingress.Hostname != "":
			out = append(out, ingress.Hostname)
		}
	}

	return out
}

// checkStorage is the check that matters most here, and the one with a real
// failure behind it.
//
// It applies a claim and — when the class binds late — a pod to consume it,
// waits for Bound, and deletes both. The cleanup runs even when the wait fails,
// because a Pending claim left behind is a volume the next run has to reason
// around.
func (r *Runner) checkStorage(ctx context.Context) Result {
	name := "a claim on " + r.opts.StorageClass + " reaches Bound"

	class, err := r.client.StorageV1().StorageClasses().Get(ctx, r.opts.StorageClass, metav1.GetOptions{})
	if err != nil {
		return Result{Name: name, Status: StatusFailed, Detail: fmt.Sprintf(
			"storage class %q: %v — layers/10-node-platform registers it",
			r.opts.StorageClass, err)}
	}

	late := class.VolumeBindingMode != nil && LateBinding(string(*class.VolumeBindingMode))

	defer r.cleanupStorageProbe(ctx, late)

	// Delete first: a probe left by a killed run would otherwise be read as
	// this run's result.
	r.cleanupStorageProbe(ctx, late)

	if applyErr := r.applyStorageProbe(ctx, late); applyErr != nil {
		return Result{Name: name, Status: StatusFailed, Detail: applyErr.Error()}
	}

	bound, waitErr := r.waitForBound(ctx)
	if waitErr != nil {
		return Result{Name: name, Status: StatusFailed, Detail: waitErr.Error()}
	}

	detail := "bound to " + bound
	if late {
		detail += " (the class binds on first consumer, so a pod was scheduled for it)"
	}

	return Result{Name: name, Status: StatusPassed, Detail: detail}
}

func (r *Runner) applyStorageProbe(ctx context.Context, late bool) error {
	size, err := resource.ParseQuantity(probeSize)
	if err != nil {
		return fmt.Errorf("probe size %q: %w", probeSize, err)
	}

	claim := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: probeClaim},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: &r.opts.StorageClass,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: size},
			},
		},
	}

	if _, err := r.client.CoreV1().PersistentVolumeClaims(probeNamespace).
		Create(ctx, claim, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create claim: %w", err)
	}

	r.opts.Logf("claim %s/%s applied", probeNamespace, probeClaim)

	if !late {
		return nil
	}

	if _, err := r.client.CoreV1().Pods(probeNamespace).
		Create(ctx, consumerPod(), metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create the claim's consumer: %w", err)
	}

	r.opts.Logf("consumer pod %s/%s applied", probeNamespace, probePod)

	return nil
}

// consumerPod is the pod that gives a WaitForFirstConsumer class something to
// bind against. It runs the pause image and is tolerant of the control-plane
// taint, because a cluster with no worker pool has nowhere else to put it.
//
// The securityContext satisfies the `restricted` Pod Security Standard. The
// first version omitted it and the API server answered with a four-clause
// warning — allowPrivilegeEscalation, capabilities, runAsNonRoot,
// seccompProfile — which the probe got away with only because the namespace
// warns rather than enforces. A probe that has to be exempted from the
// cluster's own baseline is a probe that stops working the day the baseline is
// enforced, and its warning would train the reader to ignore this output.
func consumerPod() *corev1.Pod {
	noEscalation := false
	nonRoot := true

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: probePod},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:   &nonRoot,
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Tolerations: []corev1.Toleration{{
				Key:      "node-role.kubernetes.io/control-plane",
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			}},
			Containers: []corev1.Container{{
				Name:  "pause",
				Image: probeImage,
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: &noEscalation,
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
				VolumeMounts: []corev1.VolumeMount{{
					Name:      "probe",
					MountPath: "/probe",
				}},
			}},
			Volumes: []corev1.Volume{{
				Name: "probe",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: probeClaim,
					},
				},
			}},
		},
	}
}

// pollInterval is how often the claim's phase is re-read while waiting.
const pollInterval = 3 * time.Second

// waitForBound polls the claim until it binds or the timeout passes.
//
// On timeout it reports the claim's phase and the events attached to it, which
// is where the actual reason lives — "waited 2m0s" alone sends the reader back
// to kubectl for the thing this already knows.
func (r *Runner) waitForBound(ctx context.Context) (string, error) {
	deadline := time.Now().Add(r.opts.BindTimeout)

	for {
		claim, err := r.client.CoreV1().PersistentVolumeClaims(probeNamespace).
			Get(ctx, probeClaim, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("read claim: %w", err)
		}

		if claim.Status.Phase == corev1.ClaimBound {
			return claim.Spec.VolumeName, nil
		}

		if time.Now().After(deadline) {
			return "", fmt.Errorf("claim is %s after %s%s",
				claim.Status.Phase, r.opts.BindTimeout, r.claimTrouble(ctx))
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// claimTrouble returns the claim's recent events as a suffix, or "" when there
// are none to report.
func (r *Runner) claimTrouble(ctx context.Context) string {
	events, err := r.client.CoreV1().Events(probeNamespace).List(ctx, metav1.ListOptions{
		FieldSelector: "involvedObject.name=" + probeClaim,
	})
	if err != nil || len(events.Items) == 0 {
		return ""
	}

	last := events.Items[len(events.Items)-1]

	return fmt.Sprintf(". Last event: %s: %s", last.Reason, last.Message)
}

// cleanupStorageProbe removes the probe objects, ignoring anything that is
// already gone. Errors are logged rather than returned: a cleanup failure must
// not turn a passing check into a failing one, and the operator still needs to
// know something was left behind.
func (r *Runner) cleanupStorageProbe(ctx context.Context, late bool) {
	// Deliberately not the caller's ctx for deletion: this runs from a defer,
	// including on the path where the context was cancelled, and a cancelled
	// context would skip the cleanup exactly when it is most needed.
	ctx = context.WithoutCancel(ctx)

	if late {
		if err := r.client.CoreV1().Pods(probeNamespace).
			Delete(ctx, probePod, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			r.opts.Logf("could not delete pod %s/%s: %v", probeNamespace, probePod, err)
		}
	}

	if err := r.client.CoreV1().PersistentVolumeClaims(probeNamespace).
		Delete(ctx, probeClaim, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		r.opts.Logf("could not delete claim %s/%s: %v", probeNamespace, probeClaim, err)
	}
}

// The cross-node prober.
const (
	proberPod = "cluster-smoke-crossnode"

	// dnsSelector finds the cluster's DNS pods. k8s-app=kube-dns is the label
	// CoreDNS has carried since it replaced kube-dns, and what Talos ships.
	dnsSelector = "k8s-app=kube-dns"

	// proberName is what the prober resolves. Always present, and resolved by
	// the cluster's own DNS rather than an upstream — so a reply proves the
	// query reached a CoreDNS pod, which the plan has put on another node.
	proberName = "kubernetes.default.svc.cluster.local"
)

// proberTimeout bounds the wait for the prober to finish. Short: a working
// path answers in milliseconds, and a broken one is a timeout inside the
// prober, not here.
const proberTimeout = 60 * time.Second

// checkCrossNode asks whether a pod can reach a pod on another node.
//
// The check this platform most needed and did not have. Every component can be
// Running, every node Ready, and pod-to-pod across nodes still dead — which is
// what `autoDirectNodeRoutes` on a Hetzner private network produced, for as
// long as the cluster had more than one node. A single-node cluster cannot
// exhibit it at all, which is why it survived every earlier check.
func (r *Runner) checkCrossNode(ctx context.Context) Result {
	name := "a pod reaches a pod on another node"

	nodes, err := r.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return Result{Name: name, Status: StatusFailed, Detail: "listing nodes: " + err.Error()}
	}

	states := make([]NodeState, 0, len(nodes.Items))
	for _, node := range nodes.Items {
		states = append(states, NodeState{
			Name:                      node.Name,
			Ready:                     nodeReady(node),
			ExcludedFromLoadBalancers: excludedFromLoadBalancers(node),
		})
	}

	dns, err := r.client.CoreV1().Pods(metav1.NamespaceSystem).
		List(ctx, metav1.ListOptions{LabelSelector: dnsSelector})
	if err != nil {
		return Result{Name: name, Status: StatusFailed, Detail: "listing cluster DNS pods: " + err.Error()}
	}

	dnsNodes := make([]string, 0, len(dns.Items))
	for _, pod := range dns.Items {
		if pod.Spec.NodeName != "" {
			dnsNodes = append(dnsNodes, pod.Spec.NodeName)
		}
	}

	plan := PlanCrossNode(states, dnsNodes)
	if plan.Skip != "" {
		return Result{Name: name, Status: StatusSkipped, Detail: plan.Skip}
	}

	defer r.deleteProber(ctx)

	// Delete first too: a prober left by a killed run is pinned to a node, and
	// would otherwise be read as this run's answer.
	r.deleteProber(ctx)

	if _, createErr := r.client.CoreV1().Pods(probeNamespace).
		Create(ctx, proberFor(plan.ProbeNode), metav1.CreateOptions{}); createErr != nil {
		return Result{Name: name, Status: StatusFailed,
			Detail: fmt.Sprintf("create the prober on %s: %v", plan.ProbeNode, createErr)}
	}

	r.opts.Logf("prober %s/%s applied on %s", probeNamespace, proberPod, plan.ProbeNode)

	code, detail, err := r.waitForProber(ctx)
	if err != nil {
		return Result{Name: name, Status: StatusFailed, Detail: err.Error()}
	}

	return CrossNodeVerdict(plan.ProbeNode, code, detail)
}

// proberFor builds the prober, pinned to one node.
//
// nodeName rather than an affinity rule: the choice is already made and an
// affinity the scheduler could satisfy elsewhere would silently move the probe
// onto a node where the answer means nothing.
func proberFor(node string) *corev1.Pod {
	noEscalation := false
	nonRoot := true
	// busybox's own user. runAsNonRoot needs a numeric id, because the image
	// declares no USER and the kubelet refuses to guess.
	var user int64 = 65534

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: proberPod},
		Spec: corev1.PodSpec{
			NodeName:      node,
			RestartPolicy: corev1.RestartPolicyNever,
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:   &nonRoot,
				RunAsUser:      &user,
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Tolerations: []corev1.Toleration{{
				Key:      "node-role.kubernetes.io/control-plane",
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			}},
			Containers: []corev1.Container{{
				Name:  "prober",
				Image: ProberImage,
				// The default dnsPolicy sends this at the cluster's DNS, whose
				// every replica the plan has placed on another node. nslookup's
				// exit code is the whole result, so nothing parses output.
				Command: []string{"nslookup", proberName},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: &noEscalation,
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
			}},
		},
	}
}

// waitForProber polls until the prober's container terminates, and returns its
// exit code with whatever the pod says about why.
func (r *Runner) waitForProber(ctx context.Context) (int32, string, error) {
	deadline := time.Now().Add(proberTimeout)

	for {
		pod, err := r.client.CoreV1().Pods(probeNamespace).
			Get(ctx, proberPod, metav1.GetOptions{})
		if err != nil {
			return 0, "", fmt.Errorf("read the prober: %w", err)
		}

		for _, status := range pod.Status.ContainerStatuses {
			if done := status.State.Terminated; done != nil {
				return done.ExitCode, done.Message, nil
			}
		}

		if time.Now().After(deadline) {
			// Pending past the deadline is its own answer, and a different one:
			// the pod never ran, so nothing was measured.
			return 0, "", fmt.Errorf("the prober was still %s after %s — it never ran, so "+
				"nothing about cross-node traffic was measured", pod.Status.Phase, proberTimeout)
		}

		select {
		case <-ctx.Done():
			return 0, "", ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

func (r *Runner) deleteProber(ctx context.Context) {
	ctx = context.WithoutCancel(ctx)

	if err := r.client.CoreV1().Pods(probeNamespace).
		Delete(ctx, proberPod, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		r.opts.Logf("could not delete pod %s/%s: %v", probeNamespace, proberPod, err)
	}
}

// SecretStoreResource is the API the store check reads.
//
// Pinned here because a dynamic client asks for a group, version and resource
// by name and gets an empty list for a wrong one — which reads exactly like a
// cluster with no stores. The version is the one internal/pkg/platform's store
// is created as.
var SecretStoreResource = schema.GroupVersionResource{
	Group:    "external-secrets.io",
	Version:  "v1",
	Resource: "clustersecretstores",
}

// ReadyCondition is the condition type ESO reports a usable store with.
const ReadyCondition = "Ready"

// checkSecretStores reads every ClusterSecretStore and its Ready condition.
//
// A missing CRD is not a failure: the operator may not be installed at all,
// and the dynamic client answers that with a NotFound the same way it answers
// a typo. Both are reported as "no stores", which the judgement skips on.
func (r *Runner) checkSecretStores(ctx context.Context) Result {
	if r.custom == nil {
		return SecretStoresAreReady(nil)
	}

	list, err := r.custom.Resource(SecretStoreResource).List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return SecretStoresAreReady(nil)
		}

		return Result{Name: CheckSecretStores, Status: StatusFailed,
			Detail: "listing " + SecretStoreResource.Resource + ": " + err.Error()}
	}

	stores := make([]SecretStore, 0, len(list.Items))

	for _, item := range list.Items {
		ready, reason := conditionOf(item.Object, ReadyCondition)
		stores = append(stores, SecretStore{Name: item.GetName(), Ready: ready, Reason: reason})
	}

	return SecretStoresAreReady(stores)
}

// conditionOf digs one condition out of an unstructured status.
//
// Written by hand rather than through a typed struct, because the whole point
// of the dynamic client here is not to depend on an operator's Go types for
// one field. Two checks read a condition now — the secret store's Ready and
// the network policy's Valid — and they differ only in its name.
func conditionOf(object map[string]any, want string) (bool, string) {
	status, ok := object["status"].(map[string]any)
	if !ok {
		return false, ""
	}

	conditions, ok := status["conditions"].([]any)
	if !ok {
		return false, ""
	}

	for _, entry := range conditions {
		condition, isMap := entry.(map[string]any)
		if !isMap || condition["type"] != want {
			continue
		}

		message, _ := condition["message"].(string)
		reason, _ := condition["reason"].(string)

		if condition["status"] == "True" {
			return true, ""
		}

		return false, strings.TrimSpace(reason + " " + message)
	}

	return false, ""
}

// NetworkPolicyResource is the API the policy check reads.
var NetworkPolicyResource = schema.GroupVersionResource{
	Group:    "cilium.io",
	Version:  "v2",
	Resource: "ciliumclusterwidenetworkpolicies",
}

// ValidCondition is the condition type Cilium reports an accepted policy with.
const ValidCondition = "Valid"

// checkNetworkPolicies reads every clusterwide policy and its Valid condition.
//
// The same shape as the secret-store check, and for the same reason: a custom
// resource's status is the only place this answer exists, and a typed client
// for one field would tie this to Cilium's release cycle.
func (r *Runner) checkNetworkPolicies(ctx context.Context) Result {
	if r.custom == nil {
		return NetworkPoliciesAreValid(nil)
	}

	list, err := r.custom.Resource(NetworkPolicyResource).List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return NetworkPoliciesAreValid(nil)
		}

		return Result{Name: CheckNetworkPolicies, Status: StatusFailed,
			Detail: "listing " + NetworkPolicyResource.Resource + ": " + err.Error()}
	}

	policies := make([]NetworkPolicy, 0, len(list.Items))

	for _, item := range list.Items {
		valid, reason := conditionOf(item.Object, ValidCondition)
		policies = append(policies, NetworkPolicy{Name: item.GetName(), Valid: valid, Reason: reason})
	}

	return NetworkPoliciesAreValid(policies)
}
