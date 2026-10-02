package charts

import (
	"strconv"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/imagepolicy"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"
)

// PolicyController is Sigstore's admission controller: it checks an image's
// signature, or its digest, before a pod that runs it is admitted.
//
// Installed with nothing to enforce. Its webhooks match only namespaces
// labelled policy.sigstore.dev/include=true, and nothing carries that label
// until the policies generated from internal/pkg/imagepolicy are in place —
// so on its own this chart admits everything it is asked about.
//
// The chart lags the application: 0.10.8 ships v0.13.1 while upstream has
// released v0.15.1. Nothing between them changes the CRDs or the webhook's
// arguments, but the image is the chart's own, held by the appversions check,
// and moves when the chart does.
// PolicyController is the registry key, and what a layer names when it
// installs this chart. Exported because a layer writing the key as a literal is
// the drift this package exists to remove.
const PolicyController = "policy-controller"

// PolicyControllerNamespace is where the chart runs. Sigstore's own spelling,
// which its documentation and the cleanup Job's examples assume.
const PolicyControllerNamespace = "cosign-system"

// PolicyControllerReplicas is how many webhook pods run.
//
// Two, because the chart's budget keeps one available: with a single replica
// that budget can never be met during a drain, and `task cluster:upgrade:talos`
// waits out its whole drain timeout on the node that holds it.
const PolicyControllerReplicas = 2

// The label that puts a namespace under the webhook, in the chart's spelling.
// The values template writes it into the webhooks' namespaceSelector and
// internal/pkg/layer puts it on every namespace VerifiesImages names.
const (
	PolicyControllerIncludeLabel = "policy.sigstore.dev/include"
	PolicyControllerIncludeValue = "true"
)

// PolicyControllerWebhookName is the admission webhook that refuses an image,
// as an API server's denial names it. The values template writes it, so the
// smoke check that looks for it in a denial is reading the name the chart was
// given.
const PolicyControllerWebhookName = "policy.sigstore.dev"

// VerifiesImages reports whether the pods in a chart's namespace are admitted
// through the policy-controller.
//
// Every namespace a chart here installs into, but two:
//
//   - kube-system, because the CNI, the cloud controller manager and the CSI
//     driver run there, and the webhook fails closed: with both its replicas
//     down, a Cilium agent that cannot be admitted means a node with no
//     network, and so no webhook to admit it.
//   - the policy-controller's own, for the same loop one step shorter.
func VerifiesImages(namespace string) bool {
	return namespace != NamespaceKubeSystem && namespace != PolicyControllerNamespace
}

// PolicyControllerValues is what policy-controller.yaml.tmpl is executed
// against.
type PolicyControllerValues struct {
	// NoMatchPolicy is what the webhook does with an image no policy names.
	NoMatchPolicy string
	// IncludeLabel and IncludeValue select the namespaces it admits.
	IncludeLabel string
	IncludeValue string
	// WebhookName is the admission webhook's name.
	WebhookName string
}

// policyControllerProbe renders the template offline.
func policyControllerProbe() any {
	return PolicyControllerValues{
		NoMatchPolicy: imagepolicy.ModeWarn.NoMatchPolicy(),
		IncludeLabel:  PolicyControllerIncludeLabel,
		IncludeValue:  PolicyControllerIncludeValue,
		WebhookName:   PolicyControllerWebhookName,
	}
}

// The values keys this file's settings write, in the chart's own spelling.
const (
	policyControllerReplicas = "webhook.replicaCount"
	policyControllerPriority = "webhook.priorityClass"
)

func init() {
	register(Definition{
		Key:   PolicyController,
		Layer: platform.LayerClusterServices,
		Chart: Chart{
			Name:       "policy-controller",
			Repo:       "https://sigstore.github.io/helm-charts",
			Version:    "0.10.8", // app 0.13.1
			AppVersion: "0.13.1",
			Namespace:  PolicyControllerNamespace,
		},
		Workloads: []Object{
			{Kind: Deployment, Name: "policy-controller-webhook"},
		},
		Probe: policyControllerProbe,
		Settings: []Setting{
			{
				Set:    []string{policyControllerReplicas + "=" + strconv.Itoa(PolicyControllerReplicas)},
				Expect: "replicas: " + strconv.Itoa(PolicyControllerReplicas),
				Why: "the chart's budget keeps one webhook available, so a single replica " +
					"blocks every drain of the node it runs on",
			},
			{
				Set:    []string{policyControllerPriority + "=" + PriorityClusterCritical},
				Expect: PriorityLine(PriorityClusterCritical),
				Why: "once a namespace is enforced the webhook is in the admission path for " +
					"its pods, and evicting it stops them being scheduled",
			},
		},
	})
}
