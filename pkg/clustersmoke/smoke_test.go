package clustersmoke_test

import (
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
	result, err := clustersmoke.ExternalAddresses(nil)

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
	})

	require.NoError(t, err)
	assert.Equal(t, clustersmoke.StatusPassed, result.Status)
	assert.Equal(t, "2 with an address", result.Detail)
}

func TestExternalAddresses_FailsAndPointsAtTheController(t *testing.T) {
	t.Parallel()

	result, err := clustersmoke.ExternalAddresses([]clustersmoke.LoadBalancerState{
		{Namespace: "traefik", Name: "traefik", Addresses: []string{"203.0.113.10"}},
		{Namespace: "b", Name: "pending-2"},
		{Namespace: "a", Name: "pending-1"},
	})

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
