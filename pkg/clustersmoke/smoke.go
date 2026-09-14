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
	"sort"
	"strings"
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

// Glyph is the marker this status prints with, matching pkg/pulumilog's
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
		result.Status = StatusFailed
		result.Detail = ErrNoNodes.Error()

		return result, ErrNoNodes
	}

	var notReady []string

	for _, node := range nodes {
		if !node.Ready {
			notReady = append(notReady, node.Name)
		}
	}

	if len(notReady) > 0 {
		sort.Strings(notReady)

		result.Status = StatusFailed
		result.Detail = fmt.Sprintf("%d of %d not Ready: %s",
			len(notReady), len(nodes), strings.Join(notReady, ", "))

		return result, fmt.Errorf("not Ready: %s", strings.Join(notReady, ", "))
	}

	result.Status = StatusPassed
	result.Detail = fmt.Sprintf("%d Ready", len(nodes))

	return result, nil
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
	// missing. Measured on 2026-09-14: the load balancer came up on
	// 77.42.14.120 with its services on 80 and 443 and ZERO targets, because
	// every node in a control-plane-only cluster carries the exclusion label.
	// The old check passed on it. The cloud controller manager said so in its
	// own log and nothing here read it:
	//
	//	There are no available nodes for LoadBalancer
	//	"ensure Load Balancer" service="traefik" nodes=[]
	if eligible := loadBalancerTargets(nodes); len(nodes) > 0 && eligible == 0 {
		result.Status = StatusFailed
		result.Detail = fmt.Sprintf("%d Service(s) of type LoadBalancer, and not one of the %d "+
			"nodes can be a target: every node carries %s, which Talos puts on control-plane "+
			"nodes. The load balancer answers and forwards to nothing — add a worker pool",
			len(services), len(nodes), LabelExcludeFromExternalLoadBalancers)

		return result, errors.New("no node is eligible to be a load balancer target")
	}

	var pending []string

	for _, service := range services {
		if len(service.Addresses) == 0 {
			pending = append(pending, service.Namespace+"/"+service.Name)
		}
	}

	if len(pending) > 0 {
		sort.Strings(pending)

		result.Status = StatusFailed
		result.Detail = fmt.Sprintf("%d of %d without an address: %s — the cloud controller "+
			"manager creates these, so check its logs in kube-system",
			len(pending), len(services), strings.Join(pending, ", "))

		return result, fmt.Errorf("no address: %s", strings.Join(pending, ", "))
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
