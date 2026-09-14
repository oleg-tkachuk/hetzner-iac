package clustersmoke_test

import (
	"strconv"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/clustersmoke"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNodesReady_PassesWhenEveryNodeIsReady(t *testing.T) {
	t.Parallel()

	result, err := clustersmoke.NodesReady([]clustersmoke.NodeState{
		{Name: "cp-0", Ready: true},
		{Name: "cp-1", Ready: true},
		{Name: "cp-2", Ready: true},
	})

	require.NoError(t, err)
	assert.Equal(t, clustersmoke.StatusPassed, result.Status)
	// The evidence, not just "ok": a green line that says nothing has to be
	// re-run by hand to learn anything.
	assert.Equal(t, "3 Ready", result.Detail)
}

func TestNodesReady_NamesTheNodesThatAreNot(t *testing.T) {
	t.Parallel()

	// "Ready Ready NotReady" is the case this whole package exists to judge,
	// and the one a shell pipeline got wrong.
	result, err := clustersmoke.NodesReady([]clustersmoke.NodeState{
		{Name: "cp-0", Ready: true},
		{Name: "cp-2", Ready: false},
		{Name: "cp-1", Ready: false},
	})

	require.Error(t, err)
	assert.Equal(t, clustersmoke.StatusFailed, result.Status)
	assert.Equal(t, "2 of 3 not Ready: cp-1, cp-2", result.Detail,
		"the detail must name them, sorted, so two runs compare")
}

func TestNodesReady_RefusesToPassOnAnEmptyCluster(t *testing.T) {
	t.Parallel()

	// The vacuous-truth trap: "every node is Ready" is trivially true of no
	// nodes, so a range over an empty list would report success for a cluster
	// that answered nothing.
	result, err := clustersmoke.NodesReady(nil)

	require.ErrorIs(t, err, clustersmoke.ErrNoNodes)
	assert.Equal(t, clustersmoke.StatusFailed, result.Status)
}

func TestLateBinding_IsTrueOnlyForWaitForFirstConsumer(t *testing.T) {
	t.Parallel()

	// This decides whether the storage check schedules a pod. Get it wrong for
	// hcloud-volumes, which IS WaitForFirstConsumer, and the check waits for a
	// claim that cannot bind and reports a healthy CSI as broken.
	assert.True(t, clustersmoke.LateBinding(clustersmoke.WaitForFirstConsumer))

	for _, mode := range []string{"Immediate", "", "waitforfirstconsumer", "WaitForFirstConsumers"} {
		assert.False(t, clustersmoke.LateBinding(mode), mode)
	}
}

func TestExternalAddresses_SkipsRatherThanPassesWithNothingToLookAt(t *testing.T) {
	t.Parallel()

	// The distinction the whole three-state design exists for. The dev cluster
	// has no LoadBalancer Service, so this check has nothing to prove — and
	// "passed" there is a green line that inspected an empty list.
	result, err := clustersmoke.ExternalAddresses(nil, eligibleNodes(3))

	require.NoError(t, err)
	assert.Equal(t, clustersmoke.StatusSkipped, result.Status)
	assert.Contains(t, result.Detail, "proves nothing")
}

func TestExternalAddresses_PassesOnAnAddressOfEitherKind(t *testing.T) {
	t.Parallel()

	// An IP or a hostname both mean the controller manager answered.
	result, err := clustersmoke.ExternalAddresses([]clustersmoke.LoadBalancerState{
		{Namespace: "traefik", Name: "traefik", Addresses: []string{"203.0.113.10"}},
		{Namespace: "other", Name: "svc", Addresses: []string{"lb.example.test"}},
	}, eligibleNodes(2))

	require.NoError(t, err)
	assert.Equal(t, clustersmoke.StatusPassed, result.Status)
	assert.Equal(t, "2 with an address, 2 node(s) eligible as targets", result.Detail)
}

func TestExternalAddresses_FailsAndPointsAtTheController(t *testing.T) {
	t.Parallel()

	result, err := clustersmoke.ExternalAddresses([]clustersmoke.LoadBalancerState{
		{Namespace: "traefik", Name: "traefik", Addresses: []string{"203.0.113.10"}},
		{Namespace: "b", Name: "pending-2"},
		{Namespace: "a", Name: "pending-1"},
	}, eligibleNodes(3))

	require.Error(t, err)
	assert.Equal(t, clustersmoke.StatusFailed, result.Status)
	assert.Contains(t, result.Detail, "a/pending-1, b/pending-2")
	// The remedy, because "no address" without a place to look is a fact the
	// reader has to translate before acting on.
	assert.Contains(t, result.Detail, "kube-system")
}

func TestReport_DistinguishesFailedFromSkipped(t *testing.T) {
	t.Parallel()

	// A skipped check must not make the run fail, and must not be counted as
	// an answer either.
	report := clustersmoke.Report{
		{Name: "a", Status: clustersmoke.StatusPassed},
		{Name: "b", Status: clustersmoke.StatusSkipped},
	}
	assert.False(t, report.Failed())
	assert.Equal(t, 1, report.Skipped())

	report = append(report, clustersmoke.Result{Name: "c", Status: clustersmoke.StatusFailed})
	assert.True(t, report.Failed())
	assert.Equal(t, 1, report.Skipped())
}

func TestStatusGlyph_MatchesTheLoggerVocabulary(t *testing.T) {
	t.Parallel()

	// Same marker vocabulary as pkg/pulumilog, so one run's output reads the
	// same whichever half of the repository produced the line.
	assert.Equal(t, "✔", clustersmoke.StatusPassed.Glyph())
	assert.Equal(t, "✖", clustersmoke.StatusFailed.Glyph())
	assert.Equal(t, "○", clustersmoke.StatusSkipped.Glyph())
}

func TestPlanCrossNode_ProbesFromTheNodeWithNoDNSReplica(t *testing.T) {
	t.Parallel()

	// The whole point of the plan. Ask from a node that runs a DNS replica and
	// the query can be answered locally — the check would pass on a cluster
	// whose cross-node traffic is dead, which is worse than not having it.
	plan := clustersmoke.PlanCrossNode([]clustersmoke.NodeState{
		{Name: "cp-0", Ready: true},
		{Name: "cp-1", Ready: true},
		{Name: "cp-2", Ready: true},
	}, []string{"cp-0", "cp-1"})

	assert.Empty(t, plan.Skip)
	assert.Equal(t, "cp-2", plan.ProbeNode)
}

func TestPlanCrossNode_IsDeterministic(t *testing.T) {
	t.Parallel()

	// Sorted, so two runs on one cluster probe from the same node. A coin toss
	// makes an intermittent failure impossible to compare against the last run.
	nodes := []clustersmoke.NodeState{
		{Name: "cp-2", Ready: true},
		{Name: "cp-1", Ready: true},
		{Name: "cp-0", Ready: true},
	}

	assert.Equal(t, "cp-0", clustersmoke.PlanCrossNode(nodes, []string{"cp-2"}).ProbeNode)
	assert.Equal(t, "cp-0", clustersmoke.PlanCrossNode(nodes, []string{"cp-2"}).ProbeNode)
}

func TestPlanCrossNode_SkipsWhereThereIsNothingToProve(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		nodes    []clustersmoke.NodeState
		dnsNodes []string
		says     string
	}{
		// A single-node cluster has no cross-node path, which is exactly why
		// it hid this failure for as long as the platform had one node.
		"one node": {
			[]clustersmoke.NodeState{{Name: "cp-0", Ready: true}},
			[]string{"cp-0"},
			"no cross-node path",
		},
		// Three nodes but two NotReady is the same situation.
		"one Ready of three": {
			[]clustersmoke.NodeState{
				{Name: "cp-0", Ready: true},
				{Name: "cp-1"},
				{Name: "cp-2"},
			},
			[]string{"cp-0"},
			"no cross-node path",
		},
		// Nowhere to ask from: every candidate could answer locally.
		"dns everywhere": {
			[]clustersmoke.NodeState{
				{Name: "cp-0", Ready: true},
				{Name: "cp-1", Ready: true},
			},
			[]string{"cp-0", "cp-1"},
			"would prove nothing",
		},
		"no dns at all": {
			[]clustersmoke.NodeState{
				{Name: "cp-0", Ready: true},
				{Name: "cp-1", Ready: true},
			},
			nil,
			"nothing to ask",
		},
	} {
		plan := clustersmoke.PlanCrossNode(tc.nodes, tc.dnsNodes)

		assert.Empty(t, plan.ProbeNode, name)
		assert.Contains(t, plan.Skip, tc.says, name)
	}
}

func TestCrossNodeVerdict_FailsOnANonZeroExitAndSaysWhereToLook(t *testing.T) {
	t.Parallel()

	// The failing case, which is the one that hid for thirteen hours: every
	// component Running and a third of DNS queries timing out. During the
	// incident this exact query — nslookup kubernetes.default from a pod —
	// answered "no servers could be reached", which is this non-zero exit.
	result := clustersmoke.CrossNodeVerdict("cp-2", 1, "")

	assert.Equal(t, clustersmoke.StatusFailed, result.Status)
	assert.Contains(t, result.Detail, "cp-2")
	// The remedy, because "it failed" without the command that separates node
	// reachability from endpoint reachability sends the reader back to guessing.
	assert.Contains(t, result.Detail, "cilium-health status")
	assert.Contains(t, result.Detail, "0/1")
}

func TestCrossNodeVerdict_PassesOnZeroAndSaysWhereItAskedFrom(t *testing.T) {
	t.Parallel()

	result := clustersmoke.CrossNodeVerdict("cp-2", 0, "")

	assert.Equal(t, clustersmoke.StatusPassed, result.Status)
	assert.Contains(t, result.Detail, "cp-2",
		"a pass has to name the node it asked from, or two runs cannot be compared")
}

func TestCrossNodeVerdict_CarriesWhatTheProberSaid(t *testing.T) {
	t.Parallel()

	result := clustersmoke.CrossNodeVerdict("cp-2", 1, "  connection timed out  ")

	assert.Contains(t, result.Detail, "connection timed out")
	assert.NotContains(t, result.Detail, "  connection", "the message is not trimmed")
}

// eligibleNodes is a cluster whose nodes may all be load balancer targets —
// the ordinary case, so the tests about addresses are not also about targets.
func eligibleNodes(count int) []clustersmoke.NodeState {
	nodes := make([]clustersmoke.NodeState, 0, count)

	for i := range count {
		nodes = append(nodes, clustersmoke.NodeState{
			Name:  "worker-" + strconv.Itoa(i),
			Ready: true,
		})
	}

	return nodes
}

// TestExternalAddresses_FailsWhenNoNodeCanBeATarget is today's finding, kept.
//
// The cloud controller manager did everything right: it created the load
// balancer, attached it to the private network, added services on 80 and 443,
// and published the address. It also had nowhere to send traffic, because
// Talos labels every control-plane node
// node.kubernetes.io/exclude-from-external-load-balancers and this cluster is
// three control-plane nodes.
//
// Measured against the live cluster, where the earlier version
// of this check reported "✔ 1 with an address" over a load balancer with zero
// targets — 5.39 EUR a month answering on an address and forwarding to
// nothing. The CCM had said it plainly and nothing here was reading:
//
//	There are no available nodes for LoadBalancer
func TestExternalAddresses_FailsWhenNoNodeCanBeATarget(t *testing.T) {
	t.Parallel()

	controlPlaneOnly := []clustersmoke.NodeState{
		{Name: "cp-0", Ready: true, ExcludedFromLoadBalancers: true},
		{Name: "cp-1", Ready: true, ExcludedFromLoadBalancers: true},
		{Name: "cp-2", Ready: true, ExcludedFromLoadBalancers: true},
	}

	result, err := clustersmoke.ExternalAddresses([]clustersmoke.LoadBalancerState{
		{Namespace: "traefik", Name: "traefik", Addresses: []string{"203.0.113.10"}},
	}, controlPlaneOnly)

	require.Error(t, err)
	assert.Equal(t, clustersmoke.StatusFailed, result.Status)
	// The label, so the reader can check it, and the remedy, so they can act.
	assert.Contains(t, result.Detail, clustersmoke.LabelExcludeFromExternalLoadBalancers)
	assert.Contains(t, result.Detail, "add a worker pool")
}

// TestExternalAddresses_OneEligibleNodeIsEnough keeps the check from being a
// second opinion on cluster shape.
//
// It asks whether the load balancer has anywhere to send traffic, and one
// node answers that. A cluster of three control-plane nodes and one worker is
// exactly this repository's commented-out topology, and it must pass.
func TestExternalAddresses_OneEligibleNodeIsEnough(t *testing.T) {
	t.Parallel()

	mixed := []clustersmoke.NodeState{
		{Name: "cp-0", Ready: true, ExcludedFromLoadBalancers: true},
		{Name: "cp-1", Ready: true, ExcludedFromLoadBalancers: true},
		{Name: "general-0", Ready: true},
	}

	result, err := clustersmoke.ExternalAddresses([]clustersmoke.LoadBalancerState{
		{Namespace: "traefik", Name: "traefik", Addresses: []string{"203.0.113.10"}},
	}, mixed)

	require.NoError(t, err)
	assert.Equal(t, clustersmoke.StatusPassed, result.Status)
	assert.Contains(t, result.Detail, "1 node(s) eligible")
}

// TestExternalAddresses_NoNodesAtAllIsNotAnEligibilityVerdict guards the
// vacuous reading.
//
// An empty node list means the question was not answered — a listing that
// failed, a fake client with nothing in it — and answering "no node is
// eligible" there would blame the cluster for the caller's gap. NodesReady is
// the check that fails on no nodes, and it fails loudly.
func TestExternalAddresses_NoNodesAtAllIsNotAnEligibilityVerdict(t *testing.T) {
	t.Parallel()

	result, err := clustersmoke.ExternalAddresses([]clustersmoke.LoadBalancerState{
		{Namespace: "traefik", Name: "traefik", Addresses: []string{"203.0.113.10"}},
	}, nil)

	require.NoError(t, err)
	assert.Equal(t, clustersmoke.StatusPassed, result.Status)
}
