package cni_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/charts"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/cni"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/workloads"
)

func TestSelect_DefaultsToTheOneThisClusterCanRun(t *testing.T) {
	t.Parallel()

	chosen, err := cni.Select("", clusterspec.KubeProxyDisabled)

	require.NoError(t, err)
	assert.Equal(t, "cilium", chosen.Chart)
}

func TestSelect_EveryImplementationIsPinned(t *testing.T) {
	t.Parallel()

	// A CNI naming a chart the registry does not know is a CNI with no version
	// pin, and the failure would be at apply.
	for _, name := range cni.Names() {
		chosen, err := cni.Select(name, clusterspec.KubeProxyDisabled)
		require.NoError(t, err, name)

		_, chartErr := charts.Get(chosen.Chart)
		assert.NoError(t, chartErr, "cni %s names chart %s", name, chosen.Chart)
	}
}

// TestSelect_EveryImplementationHasWorkloads lives here rather than in
// internal/pkg/layer/layertest, and that is a consequence of the design.
//
// Making the CNI selectable made it a Create component with no Chart field,
// and layertest's workloads check skips those — so cilium's workloads stopped
// being verified there. The check has to follow the data: this package is now
// where the set of possible CNI charts lives, so this is where they are paired
// with internal/pkg/workloads.
func TestSelect_EveryImplementationHasWorkloads(t *testing.T) {
	t.Parallel()

	// A chart with no declared workloads is a chart the e2e suite has nothing
	// to assert and charts:render-check has nothing to prove, and both pass
	// having checked nothing.
	for _, name := range cni.Names() {
		chosen, err := cni.Select(name, clusterspec.KubeProxyDisabled)
		require.NoError(t, err, name)

		assert.NotEmpty(t, workloads.ForChart(chosen.Chart),
			"cni %s installs chart %s, which has no workloads in internal/pkg/workloads",
			name, chosen.Chart)

		// And attributed to the layer that owns the CNI component, for the
		// same reason the pairing above lives here: layertest cannot see a
		// Chart-less component, so nothing else would notice the CNI's
		// workloads being filed under another layer. That is not
		// hypothetical — cilium's were, for months.
		attributed, named := workloads.LayerOf(chosen.Chart)
		require.True(t, named, chosen.Chart)
		assert.Equal(t, workloads.LayerNodePlatform, attributed,
			"cni %s installs chart %s, which internal/pkg/workloads attributes to %s: "+
				"the CNI component is %s's",
			name, chosen.Chart, attributed, workloads.LayerNodePlatform)
	}
}

func TestSelect_ListsTheAlternativesForAnUnknownName(t *testing.T) {
	t.Parallel()

	// A typo in a config key is otherwise indistinguishable from a CNI this
	// platform has not learned yet.
	_, err := cni.Select("calico", clusterspec.KubeProxyDisabled)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown cni")
	assert.Contains(t, err.Error(), "cilium")
}

func TestSelect_RefusesACNIThatWouldLeaveNoServiceDataplane(t *testing.T) {
	t.Parallel()

	// The constraint that makes a CNI not interchangeable. The cluster tier
	// disables kube-proxy because Cilium replaces it; a CNI that does not
	// would leave every ClusterIP blackholing, on a cluster that comes up
	// looking healthy.
	//
	// Checked through Select with a hypothetical rather than by adding a
	// second implementation: inventing values for a CNI nobody here has
	// rendered would be worse than having one.
	_, err := cni.Select("no-such-cni", true)
	require.Error(t, err, "an unknown name must fail before the kube-proxy check")

	assert.True(t, clusterspec.KubeProxyDisabled,
		"this test is about the disabled case; if the cluster tier ever enables "+
			"kube-proxy, Select's second argument is what carries that and this "+
			"assertion is the reminder to revisit the CNIs")
}

func TestSelect_AcceptsAnyCNIWhenKubeProxyRuns(t *testing.T) {
	t.Parallel()

	// The other side of the constraint: with kube-proxy running, a CNI that
	// replaces it is still fine — Cilium's kubeProxyReplacement is set from
	// the layer, not inferred here. The check is one-directional on purpose.
	chosen, err := cni.Select(cni.Default, false)

	require.NoError(t, err)
	assert.True(t, chosen.ReplacesKubeProxy)
}
