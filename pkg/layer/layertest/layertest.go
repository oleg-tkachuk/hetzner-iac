// Package layertest holds the checks every layer's component set must pass.
//
// A separate package because it takes *testing.T, which has no business in the
// code that runs against a real cluster — the same reason net/http/httptest is
// not net/http. A layer's own test calls Check and gets every invariant that
// can be decided without a cluster.
//
// These are the checks a sequence of Release calls could not have: they need
// something enumerable. That is the argument for the table, more than the
// lines it saves.
package layertest

import (
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/charts"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/layer"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/workloads"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Check asserts everything decidable about a layer's component set offline.
func Check(t *testing.T, components layer.Components) {
	t.Helper()

	require.NotEmpty(t, components, "a layer with no components deploys nothing")

	ordersDependenciesFirst(t, components)
	declaresWorkloads(t, components)
}

// ordersDependenciesFirst proves the set can be ordered at all, and that
// the order it produces puts every After before the component naming it.
//
// Ordering is checked rather than trusted because it is the thing the table
// exists to make the engine enforce. The failure it replaces cost a ten-minute
// apply: a chart that cannot be scheduled until another one has run.
func ordersDependenciesFirst(t *testing.T, components layer.Components) {
	t.Helper()

	ordered, err := layer.OrderForTest(components)
	require.NoError(t, err, "the component set cannot be ordered")
	require.Len(t, ordered, len(components), "ordering dropped a component")

	position := make(map[string]int, len(ordered))
	for i, component := range ordered {
		position[component.Key()] = i
	}

	for _, component := range components {
		for _, dependency := range component.After {
			assert.Less(t, position[dependency], position[component.Key()],
				"%s must come after %s", component.Key(), dependency)
		}
	}
}

// declaresWorkloads pairs the component set with pkg/workloads.
//
// A component with no declared workloads is a chart nothing verifies: the e2e
// suite has nothing to assert is healthy, and `task charts:render-check` has
// nothing to prove the chart still produces. Both pass, having checked nothing.
func declaresWorkloads(t *testing.T, components layer.Components) {
	t.Helper()

	for _, component := range components {
		// A component that is not a chart produces no workload a chart
		// renderer could find: the Secret both hcloud charts read is one, the
		// ClusterIssuer is another. Nothing to pair them with.
		if component.Chart == "" {
			continue
		}

		chart, err := charts.Get(component.Chart)
		require.NoError(t, err, component.Chart)

		assert.NotEmpty(t, workloads.ForChart(component.Chart),
			"chart %s (%s) has no workloads in pkg/workloads: nothing verifies it was deployed",
			component.Chart, chart.Name)
	}
}
