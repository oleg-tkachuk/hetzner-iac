package clustersmoke_test

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clustersmoke"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"

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

	// Same marker vocabulary as internal/pkg/pulumilog, so one run's output reads the
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

// errorReturn matches a judgement handing back a Result and a non-nil error.
var errorReturn = regexp.MustCompile(`return result, (?:err|Err|errors\.|fmt\.Errorf)`)

// TestJudgements_ReturnNoErrorWithoutAFailingStatus holds the invariant the
// runner relies on.
//
// run.go keeps the Result and blanks the error, because the two carry the same
// fact. That is only true while every error also carries StatusFailed — and a
// judgement that returned one without it would make the report say "passed"
// with the error thrown away, which is the worst shape a check can have.
//
// Held structurally rather than by inspection: failed() sets the status and
// returns the pair, so the two cannot disagree. This test is about the one
// thing failed() cannot enforce — that nobody returns an error around it.
func TestJudgements_ReturnNoErrorWithoutAFailingStatus(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("smoke.go")
	require.NoError(t, err)

	var found int

	for i, line := range strings.Split(string(raw), "\n") {
		if !errorReturn.MatchString(line) {
			continue
		}

		found++

		assert.Contains(t, line, "return result, err",
			"smoke.go:%d returns an error beside a Result without going through failed(), "+
				"so the status and the error can disagree:\n\t%s",
			i+1, strings.TrimSpace(line))
	}

	// failed() itself is the one legitimate `return result, err`, and finding
	// nothing would mean the pattern stopped matching rather than that the
	// code is clean.
	assert.Equal(t, 1, found,
		"expected exactly one `return result, err` — failed()'s own — and found %d", found)
}

// TestExternalMetricsServed covers the three answers, and the middle one is
// why the check exists.
//
// KEDA's APIService is created with no caBundle and the operator patches it in
// afterwards. Helm waits for workloads, not for APIServices, so the release
// reports success while the group is registered and unanswerable — and nothing
// else in this repository looks.
func TestExternalMetricsServed(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		groups     []string
		failure    string
		want       clustersmoke.Status
		wantDetail string
	}{
		"served": {
			groups: []string{"apps", clustersmoke.ExternalMetricsGroup},
			want:   clustersmoke.StatusPassed,
		},
		"registered and not answering": {
			groups: []string{"apps"},
			failure: "unable to retrieve the complete list of server APIs: " +
				clustersmoke.ExternalMetricsGroup + "/v1beta1: the server is currently unable to handle the request",
			want:       clustersmoke.StatusFailed,
			wantDetail: "apiservice",
		},
		// Off is the default, so this must not be a failure: a cluster that
		// never asked for KEDA is not a broken cluster.
		"absent because KEDA is not installed": {
			groups: []string{"apps", "metrics.k8s.io"},
			want:   clustersmoke.StatusSkipped,
		},
		// Discovery can fail for a group that is nothing to do with this one.
		"another group is failing": {
			groups:  []string{"apps", clustersmoke.ExternalMetricsGroup},
			failure: "unable to retrieve the complete list of server APIs: custom.metrics.k8s.io/v1beta1",
			want:    clustersmoke.StatusPassed,
		},
	}

	for name, one := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			result := clustersmoke.ExternalMetricsServed(one.groups, one.failure)

			assert.Equal(t, one.want, result.Status)
			assert.Equal(t, clustersmoke.CheckExternalMetrics, result.Name)
			assert.NotEmpty(t, result.Detail, "a result with no detail says nothing a reader can act on")

			if one.wantDetail != "" {
				assert.Contains(t, strings.ToLower(result.Detail), one.wantDetail,
					"the failure must name where to look")
			}
		})
	}
}

// TestDataVolumesAreRetained_SkipsWhenNothingClaimsToHoldData is the state
// this check spends most of its life in, and reporting it as a pass would be a
// green line that inspected nothing.
func TestDataVolumesAreRetained_SkipsWhenNothingClaimsToHoldData(t *testing.T) {
	t.Parallel()

	result := clustersmoke.DataVolumesAreRetained(nil, 0)

	assert.Equal(t, clustersmoke.StatusSkipped, result.Status)
	assert.Contains(t, result.Detail, platform.DataNamespaceLabel,
		"the detail must name the label, or an operator cannot act on the skip")
}

// TestDataVolumesAreRetained_FailsOnAClassThatDeletes is the failure the whole
// taxonomy exists for: a database on the default class.
func TestDataVolumesAreRetained_FailsOnAClassThatDeletes(t *testing.T) {
	t.Parallel()

	result := clustersmoke.DataVolumesAreRetained([]clustersmoke.DataVolume{
		{Namespace: "postgres", Name: "data-pg-0", Class: platform.StorageClass, Reclaim: "Delete"},
	}, 1)

	assert.Equal(t, clustersmoke.StatusFailed, result.Status)
	assert.Contains(t, result.Detail, "postgres/data-pg-0")
	assert.Contains(t, result.Detail, platform.StorageClassDatabase,
		"the detail must name the class to move to, not only the one to move off")
}

// TestDataVolumesAreRetained_PassesWhenEveryClaimRetains keeps the pass honest:
// it says how much was looked at.
func TestDataVolumesAreRetained_PassesWhenEveryClaimRetains(t *testing.T) {
	t.Parallel()

	result := clustersmoke.DataVolumesAreRetained([]clustersmoke.DataVolume{
		{Namespace: "postgres", Name: "data-pg-0", Class: platform.StorageClassDatabase, Reclaim: "Retain"},
		{Namespace: "postgres", Name: "data-pg-1", Class: platform.StorageClassDatabase, Reclaim: "Retain"},
	}, 1)

	assert.Equal(t, clustersmoke.StatusPassed, result.Status)
	assert.Contains(t, result.Detail, "2 claim(s)")
}

// TestDataVolumesAreRetained_JudgesEveryClaimNotTheFirst: a report naming one
// of three sends an operator back for a second run.
func TestDataVolumesAreRetained_JudgesEveryClaimNotTheFirst(t *testing.T) {
	t.Parallel()

	result := clustersmoke.DataVolumesAreRetained([]clustersmoke.DataVolume{
		{Namespace: "a", Name: "one", Class: platform.StorageClass, Reclaim: "Delete"},
		{Namespace: "b", Name: "two", Class: platform.StorageClassDatabase, Reclaim: "Retain"},
		{Namespace: "c", Name: "three", Class: platform.StorageClass, Reclaim: "Delete"},
	}, 3)

	require.Equal(t, clustersmoke.StatusFailed, result.Status)
	assert.Contains(t, result.Detail, "a/one")
	assert.Contains(t, result.Detail, "c/three")
	assert.NotContains(t, result.Detail, "b/two")
}

// TestSecretStoresAreReady_SkipsWhenNoneExist: the operator ships with this
// platform and is pointed at a backend only when one is configured, so no
// store is a decision rather than a fault.
func TestSecretStoresAreReady_SkipsWhenNoneExist(t *testing.T) {
	t.Parallel()

	result := clustersmoke.SecretStoresAreReady(nil)

	assert.Equal(t, clustersmoke.StatusSkipped, result.Status)
	assert.Contains(t, result.Detail, "ClusterSecretStore")
}

// TestSecretStoresAreReady_FailsWithTheOperatorsOwnReason is the failure this
// check exists for: everything downstream reports a problem that is not about
// secrets, three steps from the credential that is wrong.
func TestSecretStoresAreReady_FailsWithTheOperatorsOwnReason(t *testing.T) {
	t.Parallel()

	result := clustersmoke.SecretStoresAreReady([]clustersmoke.SecretStore{
		{Name: "pulumi-esc", Ready: false, Reason: "InvalidProviderConfig unauthorized"},
	})

	require.Equal(t, clustersmoke.StatusFailed, result.Status)
	assert.Contains(t, result.Detail, "pulumi-esc")
	assert.Contains(t, result.Detail, "unauthorized",
		"the operator's own reason is the only thing here that points at the cause")
	assert.Contains(t, result.Detail, "absent rather than stale",
		"the detail must say what the failure looks like downstream")
}

// TestSecretStoresAreReady_SaysSoWhenThereIsNoConditionYet keeps a store that
// has never reconciled from reading as ready.
func TestSecretStoresAreReady_SaysSoWhenThereIsNoConditionYet(t *testing.T) {
	t.Parallel()

	result := clustersmoke.SecretStoresAreReady([]clustersmoke.SecretStore{
		{Name: "pulumi-esc"},
	})

	require.Equal(t, clustersmoke.StatusFailed, result.Status)
	assert.Contains(t, result.Detail, "no Ready condition yet")
}

// TestSecretStoresAreReady_JudgesEveryStore: naming one of three sends an
// operator back for a second run.
func TestSecretStoresAreReady_JudgesEveryStore(t *testing.T) {
	t.Parallel()

	result := clustersmoke.SecretStoresAreReady([]clustersmoke.SecretStore{
		{Name: "one", Ready: true},
		{Name: "two", Reason: "Invalid"},
		{Name: "three", Reason: "Unauthorized"},
	})

	require.Equal(t, clustersmoke.StatusFailed, result.Status)
	assert.Contains(t, result.Detail, "two")
	assert.Contains(t, result.Detail, "three")
	assert.NotContains(t, result.Detail, "one:")
}

// TestSecretStoresAreReady_PassesAndSaysHowMany keeps the pass honest.
func TestSecretStoresAreReady_PassesAndSaysHowMany(t *testing.T) {
	t.Parallel()

	result := clustersmoke.SecretStoresAreReady([]clustersmoke.SecretStore{
		{Name: "pulumi-esc", Ready: true},
	})

	assert.Equal(t, clustersmoke.StatusPassed, result.Status)
	assert.Contains(t, result.Detail, "1 store(s) ready")
}
