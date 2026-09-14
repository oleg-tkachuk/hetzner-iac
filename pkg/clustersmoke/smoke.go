// Package clustersmoke answers one question about a cluster this repository
// built: can it actually run a workload?
//
// Three checks, each proving that a different piece of cluster-tier wiring is
// not merely installed but working — nodes Ready proves the CNI, a claim
// reaching Bound proves the CSI, an address on a LoadBalancer Service proves
// the cloud controller manager. A cluster can report every component Running
// and fail all three, which is not hypothetical here: `pulumi up` went green
// on a three-node cluster whose CSI controller was in CrashLoopBackOff, and
// nothing said so, because nothing asked for a volume.
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
}

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
func ExternalAddresses(services []LoadBalancerState) (Result, error) {
	result := Result{Name: "every LoadBalancer Service has an address"}

	if len(services) == 0 {
		result.Status = StatusSkipped
		result.Detail = "no Service of type LoadBalancer exists, so this proves nothing " +
			"about the cloud controller manager"

		return result, nil
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
	result.Detail = fmt.Sprintf("%d with an address", len(services))

	return result, nil
}
