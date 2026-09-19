// Package clustersmoke answers one question about a cluster this repository
// built: can it actually run a workload?
//
// Four checks, each proving that a different piece of cluster-tier wiring is
// not merely installed but working — nodes Ready proves the CNI is installed, a
// pod reaching a pod on another node proves it actually routes, a claim
// reaching Bound proves the CSI, an address on a LoadBalancer Service proves
// the cloud controller manager.
//
// A cluster can report every component Running and fail all four, and this
// repository has now watched it happen twice. `pulumi up` went green on a
// three-node cluster whose CSI controller was in CrashLoopBackOff, because
// nothing asked for a volume. And the cause of THAT was pod-to-pod traffic
// across nodes having no route at all — every node Ready, every pod Running,
// and a third of DNS queries timing out — which is why the second check
// exists and why it is the one a single-node cluster cannot exercise.
//
// The judgements are pure functions and the execution is client-go. That split
// is the point: deciding whether "Ready Ready NotReady" is a healthy cluster is
// a rule worth a test, and a check nobody can test is a check nobody should
// trust.
//
// THREE STATES, NOT TWO. A check that cannot run reports Skipped rather than
// Passed. The cluster currently has no LoadBalancer Service at all, so the
// controller-manager check has nothing to look at — and reporting that as
// success would be a green line that inspected nothing, which is worse than no
// check.
package clustersmoke

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"
)

// Status is a check's verdict.
type Status int

const (
	// StatusPassed means the check ran and the cluster satisfied it.
	StatusPassed Status = iota
	// StatusFailed means the check ran and the cluster did not.
	StatusFailed
	// StatusSkipped means the check could not run, so it proves nothing. It is
	// deliberately distinct from StatusPassed: see the package comment.
	StatusSkipped
)

// Glyph is the marker this status prints with, matching internal/pkg/pulumilog's
// vocabulary so one run's output reads the same whoever produced the line.
func (s Status) Glyph() string {
	switch s {
	case StatusPassed:
		return "✔"
	case StatusFailed:
		return "✖"
	case StatusSkipped:
		return "○"
	default:
		return "?"
	}
}

// Result is one check's outcome.
type Result struct {
	// Name is what was checked, phrased as the property that should hold.
	Name string
	// Status is the verdict.
	Status Status
	// Detail says what was observed. Always populated, including on success:
	// "3 nodes Ready" is the evidence, and a check that reports only "ok" has
	// to be re-run by hand to learn anything.
	Detail string
}

// Report is every check's outcome from one run.
type Report []Result

// Failed reports whether any check failed. Skipped checks are not failures:
// they are the absence of an answer, and the caller decides what that is worth.
func (r Report) Failed() bool {
	for _, result := range r {
		if result.Status == StatusFailed {
			return true
		}
	}

	return false
}

// Skipped counts the checks that could not run, so a caller can say "2 passed,
// 1 skipped" rather than implying three answers.
func (r Report) Skipped() int {
	var n int

	for _, result := range r {
		if result.Status == StatusSkipped {
			n++
		}
	}

	return n
}

// NodeState is one node reduced to what the check needs.
type NodeState struct {
	Name string
	// Ready is the node's Ready condition being True. A node with no Ready
	// condition at all is not Ready — the zero value is the safe reading.
	Ready bool
	// ExcludedFromLoadBalancers is the node carrying
	// node.kubernetes.io/exclude-from-external-load-balancers. Talos puts it on
	// every control-plane node, and a cloud controller manager will not make
	// such a node a target of an external load balancer.
	ExcludedFromLoadBalancers bool
}

// CheckExternalAddresses names the load balancer check.
//
// Named because three places spell it: the check itself, the error path in the
// runner when listing fails, and docs/operations.md. Two of them already
// disagreed — the runner's listing error still said "every LoadBalancer
// Service has an address" after the check itself started asking about targets,
// so one check reported under two names depending on how it failed.
const CheckExternalAddresses = "every LoadBalancer Service has an address and somewhere to send it"

// LabelExcludeFromExternalLoadBalancers is the upstream label that keeps a node
// out of every external load balancer's target list.
//
// Named rather than spelled twice: the runner reads it off the node and this
// package reasons about it, and a misspelling in either place would read as
// "no node is excluded" — the answer that hides the failure.
const LabelExcludeFromExternalLoadBalancers = "node.kubernetes.io/exclude-from-external-load-balancers"

// ErrNoNodes is returned when the cluster reports no nodes. Distinct from
// "some node is not Ready": an empty list means the question was not answered,
// and an empty range would otherwise pass vacuously.
var ErrNoNodes = errors.New("cluster reports no nodes")

// NodesReady is the CNI judgement.
//
// Every node has to be Ready, and there has to be at least one. Talos leaves a
// node NotReady until a CNI is installed, which makes this the cheapest
// end-to-end proof that layers/10-node-platform actually took: the kubelet is
// talking to the API server and the pod network exists.
func NodesReady(nodes []NodeState) (Result, error) {
	result := Result{Name: "every node is Ready"}

	if len(nodes) == 0 {
		return failed(result, ErrNoNodes.Error(), ErrNoNodes)
	}

	var notReady []string

	for _, node := range nodes {
		if !node.Ready {
			notReady = append(notReady, node.Name)
		}
	}

	if len(notReady) > 0 {
		sort.Strings(notReady)

		return failed(result,
			fmt.Sprintf("%d of %d not Ready: %s",
				len(notReady), len(nodes), strings.Join(notReady, ", ")),
			fmt.Errorf("not Ready: %s", strings.Join(notReady, ", ")))
	}

	result.Status = StatusPassed
	result.Detail = fmt.Sprintf("%d Ready", len(nodes))

	return result, nil
}

// ExternalMetricsGroup is the aggregated API group KEDA's metrics server
// serves. Named because two things spell it: the check and the failure text.
const ExternalMetricsGroup = "external.metrics.k8s.io"

// CheckExternalMetrics names the check, in one place, for the same reason
// CheckExternalAddresses is named.
const CheckExternalMetrics = "the external metrics API answers"

// ExternalMetricsServed is the judgement on an aggregated API group, from what
// discovery says about it.
//
// The failure this exists for is specific and self-healing most of the time,
// which is why nothing else notices it. KEDA's APIService is created with no
// caBundle: the operator patches it in afterwards, and until it does, the
// group is registered and unanswerable. Helm does not wait for an APIService —
// it waits for workloads — so the release reports success either way, and a
// group that stays broken shows up later as an autoscaler that never acts and
// a `kubectl` that has become slow.
//
// Three answers, and the middle one is the point:
//
//   - discovery failed for this group: it is registered and not answering.
//   - the group is served: KEDA is installed and working.
//   - the group is absent: nothing registered it, so KEDA is not installed —
//     skipped rather than failed, because kedaEnabled is off by default.
func ExternalMetricsServed(groups []string, discoveryFailure string) Result {
	result := Result{Name: CheckExternalMetrics}

	if strings.Contains(discoveryFailure, ExternalMetricsGroup) {
		result.Status = StatusFailed
		result.Detail = fmt.Sprintf(
			"%s is registered and does not answer: %s. "+
				"The APIService carries the CA bundle KEDA's operator patches into it, so "+
				"`kubectl get apiservice v1beta1.%s -o yaml` and the operator's logs are "+
				"the two places to look",
			ExternalMetricsGroup, strings.TrimSpace(discoveryFailure), ExternalMetricsGroup)

		return result
	}

	if slices.Contains(groups, ExternalMetricsGroup) {
		result.Status = StatusPassed
		result.Detail = ExternalMetricsGroup + " is served"

		return result
	}

	result.Status = StatusSkipped
	result.Detail = "nothing serves " + ExternalMetricsGroup + ", so KEDA is not installed"

	return result
}

// WaitForFirstConsumer is the binding mode that defers volume creation until a
// pod actually schedules against the claim.
const WaitForFirstConsumer = "WaitForFirstConsumer"

// LateBinding reports whether a storage class defers binding to a consumer.
//
// This decides whether the storage check needs a pod. hcloud-volumes is
// WaitForFirstConsumer, so a bare PersistentVolumeClaim stays Pending forever
// on a perfectly healthy cluster — a check that applied only a claim and waited
// for Bound would fail every time and say the CSI was broken when it was not.
func LateBinding(volumeBindingMode string) bool {
	return volumeBindingMode == WaitForFirstConsumer
}

// LoadBalancerState is one Service of type LoadBalancer, reduced to whether the
// cloud controller manager has given it an address.
type LoadBalancerState struct {
	Namespace string
	Name      string
	// Addresses are the ingress entries on the Service's status: an IP, a
	// hostname, or nothing while the CCM is still working or broken.
	Addresses []string
}

// ExternalAddresses is the cloud-controller-manager judgement.
//
// Skipped rather than passed when there is no LoadBalancer Service: the CCM's
// route controller may be running perfectly and there is simply nothing for it
// to have done. Saying "passed" there would be a green line that looked at an
// empty list.
func ExternalAddresses(services []LoadBalancerState, nodes []NodeState) (Result, error) {
	result := Result{Name: CheckExternalAddresses}

	if len(services) == 0 {
		result.Status = StatusSkipped
		result.Detail = "no Service of type LoadBalancer exists, so this proves nothing " +
			"about the cloud controller manager"

		return result, nil
	}

	// An address on its own is not reachability, and this is the half that was
	// missing. Measured: the load balancer came up with an address, its
	// services on 80 and 443, and ZERO targets, because every node in a
	// control-plane-only cluster carries the exclusion label. The address
	// itself is left out on purpose — it was this project's own, it is not
	// reservable so it means nothing after a rebuild, and the claim does not
	// rest on it.
	// The old check passed on it. The cloud controller manager said so in its
	// own log and nothing here read it:
	//
	//	There are no available nodes for LoadBalancer
	//	"ensure Load Balancer" service="traefik" nodes=[]
	if eligible := loadBalancerTargets(nodes); len(nodes) > 0 && eligible == 0 {
		return failed(result,
			fmt.Sprintf("%d Service(s) of type LoadBalancer, and not one of the %d "+
				"nodes can be a target: every node carries %s, which Talos puts on control-plane "+
				"nodes. The load balancer answers and forwards to nothing — add a worker pool",
				len(services), len(nodes), LabelExcludeFromExternalLoadBalancers),
			errors.New("no node is eligible to be a load balancer target"))
	}

	var pending []string

	for _, service := range services {
		if len(service.Addresses) == 0 {
			pending = append(pending, service.Namespace+"/"+service.Name)
		}
	}

	if len(pending) > 0 {
		sort.Strings(pending)

		return failed(result,
			fmt.Sprintf("%d of %d without an address: %s — the cloud controller "+
				"manager creates these, so check its logs in kube-system",
				len(pending), len(services), strings.Join(pending, ", ")),
			fmt.Errorf("no address: %s", strings.Join(pending, ", ")))
	}

	result.Status = StatusPassed
	result.Detail = fmt.Sprintf("%d with an address, %d node(s) eligible as targets",
		len(services), loadBalancerTargets(nodes))

	return result, nil
}

// loadBalancerTargets counts the nodes a cloud controller manager may put
// behind an external load balancer.
//
// Readiness is deliberately not part of it. A node that is temporarily
// NotReady is still a target the CCM keeps; the exclusion label is the
// permanent condition, and conflating the two would turn a transient reboot
// into this check's verdict.
func loadBalancerTargets(nodes []NodeState) int {
	eligible := 0

	for _, node := range nodes {
		if !node.ExcludedFromLoadBalancers {
			eligible++
		}
	}

	return eligible
}

// ProberImage is the container the cross-node check runs.
//
// busybox because it has nslookup and an exit code; the check reads that exit
// code off the pod's status rather than exec-ing in, so nothing here parses a
// stream. Pinned, for the reason the policy pack states about every image.
const ProberImage = "busybox:1.37"

// CrossNodePlan is where the cross-node check can run, or why it cannot.
type CrossNodePlan struct {
	// ProbeNode is the node to schedule the prober on: Ready, and holding no
	// cluster-DNS replica, so every DNS backend it can reach is on another
	// node and the query has to cross a node boundary to be answered.
	ProbeNode string
	// Skip is the reason there is nothing to prove, empty when there is.
	Skip string
}

// PlanCrossNode decides where to probe from.
//
// The judgement, separated from the doing: which node makes the test mean
// something is a rule worth a test of its own, and getting it wrong produces a
// check that passes on a cluster whose cross-node traffic is dead — by asking a
// DNS replica that happens to be local.
//
// Skipped rather than failed in both directions it cannot run. One node cannot
// have a cross-node path at all, and a cluster whose DNS sits on every Ready
// node leaves nowhere to ask from.
func PlanCrossNode(nodes []NodeState, dnsNodes []string) CrossNodePlan {
	ready := make([]string, 0, len(nodes))

	for _, node := range nodes {
		if node.Ready {
			ready = append(ready, node.Name)
		}
	}

	if len(ready) < 2 {
		return CrossNodePlan{Skip: fmt.Sprintf(
			"%d node is Ready, so there is no cross-node path to test", len(ready))}
	}

	if len(dnsNodes) == 0 {
		return CrossNodePlan{Skip: "no cluster-DNS pod found, so there is nothing to ask across a node"}
	}

	hasDNS := make(map[string]bool, len(dnsNodes))
	for _, node := range dnsNodes {
		hasDNS[node] = true
	}

	// Sorted, so two runs on the same cluster probe from the same node and a
	// failure is comparable rather than a coin toss.
	sort.Strings(ready)

	for _, node := range ready {
		if !hasDNS[node] {
			return CrossNodePlan{ProbeNode: node}
		}
	}

	return CrossNodePlan{Skip: fmt.Sprintf(
		"every one of the %d Ready nodes runs a cluster-DNS replica, so a query "+
			"could be answered locally and would prove nothing about crossing a node", len(ready))}
}

// CrossNodeVerdict turns the prober's exit code into a result.
//
// Zero means a DNS reply came back from a replica on another node, which is
// the whole claim: a pod reached a pod across a node boundary. Anything else is
// the failure this check exists for, and it is the one that hid for thirteen
// hours — every component Running, and a third of DNS queries timing out.
func CrossNodeVerdict(probeNode string, exitCode int32, detail string) Result {
	result := Result{Name: "a pod reaches a pod on another node"}

	if exitCode == 0 {
		result.Status = StatusPassed
		result.Detail = "cluster DNS answered from another node, asked from " + probeNode

		return result
	}

	result.Status = StatusFailed
	result.Detail = fmt.Sprintf(
		"from %s the cluster DNS on another node did not answer (exit %d). "+
			"Pod-to-pod across nodes is the first thing to check: "+
			"`kubectl -n kube-system exec ds/cilium -c cilium-agent -- cilium-health status` "+
			"reports node and endpoint reachability separately, and endpoints at 0/1 "+
			"with nodes at 1/1 means the hosts route and the pods do not",
		probeNode, exitCode)

	if strings.TrimSpace(detail) != "" {
		result.Detail += ". Prober said: " + strings.TrimSpace(detail)
	}

	return result
}

// failed pairs a judgement's verdict with its error, so the two cannot
// disagree.
//
// Every judgement returns (Result, error) for the same fact: the Result is
// what `tools/smoke` prints, the error is what a caller that wants to stop
// reads. internal/pkg/clustersmoke's own runner keeps the Result and blanks
// the error, which is safe only while every error also carries a failing
// status — and that was four separate places remembering to set it.
//
// One constructor instead. A judgement that returns an error through this
// cannot report a pass, and TestJudgements_ReturnNoErrorWithoutAFailingStatus
// holds that nothing returns one any other way.
func failed(result Result, detail string, err error) (Result, error) {
	result.Status = StatusFailed
	result.Detail = detail

	return result, err
}

// CheckDataVolumes is the name of the check below.
const CheckDataVolumes = "volumes holding data are on a class that retains them"

// ReclaimDelete is the policy that takes the volume with the claim.
//
// Spelled here rather than imported from k8s.io/api so this file stays
// readable on its own; the value is the API's and cannot change without a
// Kubernetes version that renames it.
const ReclaimDelete = "Delete"

// DataVolume is one claim in a namespace that says it holds data.
type DataVolume struct {
	// Namespace and Name identify the claim.
	Namespace string
	Name      string
	// Class is the storage class it is bound to, and Reclaim that class's
	// policy. A claim with no class named is bound to the default one, which
	// is the case this check exists for.
	Class   string
	Reclaim string
}

// DataVolumesAreRetained is the judgement on the claims in namespaces labelled
// as holding data.
//
// The rule it enforces is in internal/pkg/platform: two storage classes, one
// per class of DATA, and a database's volume belongs on the one that retains.
// Without a check the taxonomy is a sentence in a document — a chart that omits
// storageClassName gets the DEFAULT class, which is the one that deletes, and
// nothing says a word until somebody deletes the claim.
//
// Three answers, and the middle one is why this is a function:
//
//   - no labelled namespace: skipped. Nothing here holds data yet, and a
//     failure would train an operator to ignore the check before it has
//     anything to say.
//   - a claim on a Delete class: failed, naming the claim and the class.
//   - everything retained: passed.
func DataVolumesAreRetained(volumes []DataVolume, labelled int) Result {
	result := Result{Name: CheckDataVolumes}

	if labelled == 0 {
		result.Status = StatusSkipped
		result.Detail = "no namespace carries " + platform.DataNamespaceLabel +
			", so nothing claims to hold data that cannot be rebuilt"

		return result
	}

	var wrong []string

	for _, volume := range volumes {
		if volume.Reclaim != ReclaimDelete {
			continue
		}

		wrong = append(wrong, fmt.Sprintf("%s/%s on %s", volume.Namespace, volume.Name, volume.Class))
	}

	if len(wrong) > 0 {
		result.Status = StatusFailed
		result.Detail = fmt.Sprintf(
			"%s: the class reclaims %s, so deleting the claim deletes the volume. "+
				"Name %s in the claim instead — see internal/pkg/platform",
			strings.Join(wrong, ", "), ReclaimDelete, platform.StorageClassDatabase)

		return result
	}

	result.Status = StatusPassed
	result.Detail = fmt.Sprintf("%d claim(s) in %d labelled namespace(s), all on a retaining class",
		len(volumes), labelled)

	return result
}
