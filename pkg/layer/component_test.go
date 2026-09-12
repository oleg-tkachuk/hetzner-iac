package layer_test

import (
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/layer"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func chartKeys(components layer.Components) []string {
	out := make([]string, 0, len(components))
	for _, component := range components {
		out = append(out, component.Chart)
	}

	return out
}

func TestOrder_PutsDependenciesFirst(t *testing.T) {
	t.Parallel()

	// The shape 60-observability has: three components behind one.
	ordered, err := layer.OrderForTest(layer.Components{
		{Chart: "loki", After: []string{"kube-prometheus-stack"}},
		{Chart: "alloy", After: []string{"kube-prometheus-stack"}},
		{Chart: "kube-prometheus-stack"},
		{Chart: "tempo", After: []string{"kube-prometheus-stack"}},
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"kube-prometheus-stack", "alloy", "loki", "tempo"}, chartKeys(ordered),
		"the dependency first, then the rest by chart key")
}

func TestOrder_IsDeterministic(t *testing.T) {
	t.Parallel()

	// Map iteration order in Go is randomised, so an ordering that walked a
	// map would produce a different sequence per run — and every preview would
	// show a diff that means nothing. Ties break by chart key.
	first, err := layer.OrderForTest(layer.Components{
		{Chart: "tempo"}, {Chart: "alloy"}, {Chart: "loki"},
	})
	require.NoError(t, err)

	for range 20 {
		again, againErr := layer.OrderForTest(layer.Components{
			{Chart: "tempo"}, {Chart: "alloy"}, {Chart: "loki"},
		})
		require.NoError(t, againErr)
		require.Equal(t, chartKeys(first), chartKeys(again))
	}

	assert.Equal(t, []string{"alloy", "loki", "tempo"}, chartKeys(first))
}

func TestOrder_RefusesACycle(t *testing.T) {
	t.Parallel()

	// A cycle cannot be deployed in any order, and left to Pulumi it surfaces
	// as a resource graph error naming URNs rather than charts.
	_, err := layer.OrderForTest(layer.Components{
		{Chart: "loki", After: []string{"tempo"}},
		{Chart: "tempo", After: []string{"loki"}},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "dependency cycle")
}

func TestOrder_RefusesADependencyOutsideTheSet(t *testing.T) {
	t.Parallel()

	// Silently ignoring it is the dangerous alternative: the component is
	// created with no dependency at all, which is exactly the race the After
	// field exists to remove.
	_, err := layer.OrderForTest(layer.Components{
		{Chart: "loki", After: []string{"kube-prometheus-stack"}},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in this set")
}

func TestOrder_RefusesAnUnpinnedChart(t *testing.T) {
	t.Parallel()

	// The registry is what carries the version, so a chart key it does not
	// know is a chart with no pin. Caught here rather than at apply.
	_, err := layer.OrderForTest(layer.Components{{Chart: "not-a-chart"}})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not-a-chart")
}

func TestOrder_RefusesADuplicateChart(t *testing.T) {
	t.Parallel()

	// Two components for one chart would be two Helm releases with the same
	// name, which Helm rejects halfway through an apply.
	_, err := layer.OrderForTest(layer.Components{{Chart: "loki"}, {Chart: "loki"}})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "twice")
}

func TestOrder_RefusesAComponentThatDeploysNothing(t *testing.T) {
	t.Parallel()

	// Neither a chart nor a Create function is a name in the order that
	// creates nothing and looks like it should.
	_, err := layer.OrderForTest(layer.Components{{}})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "neither a chart nor a Create function")
}

func TestOrder_RefusesAComponentThatIsBoth(t *testing.T) {
	t.Parallel()

	// A component with both would deploy the chart and silently skip Create.
	_, err := layer.OrderForTest(layer.Components{{
		Chart:  "loki",
		Create: func(*layer.Runner, []pulumi.Resource) (pulumi.Resource, error) { return nil, nil },
	}})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "both a chart and a Create function")
}

func TestOrder_RefusesACreateComponentWithNoName(t *testing.T) {
	t.Parallel()

	// A Create component has no chart key to fall back on, so nothing else
	// could name it in After.
	_, err := layer.OrderForTest(layer.Components{{
		Create: func(*layer.Runner, []pulumi.Resource) (pulumi.Resource, error) { return nil, nil },
	}})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no Name")
}

func TestDependsOn(t *testing.T) {
	t.Parallel()

	// No dependencies must produce no option at all. `pulumi.DependsOn(nil)`
	// is an option that says nothing, and handing one to every resource made
	// the three callers disagree about whether to guard the call.
	assert.Nil(t, layer.DependsOn(nil))
	assert.Nil(t, layer.DependsOn([]pulumi.Resource{}))
	assert.Len(t, layer.DependsOn([]pulumi.Resource{nil}), 1)
}

func TestDeploy_HandsTheRenderedValuesToTheRelease(t *testing.T) {
	setStackRef(t, "acme/hetzner-cluster/prod")

	m := newMocks()

	require.NoError(t, run(t, m, func(runner *layer.Runner) error {
		_, err := runner.Deploy(layer.Components{
			{
				Chart: "cilium",
				ValuesYAML: func(*layer.Runner) pulumi.AssetOrArchiveArrayInput {
					return pulumi.AssetOrArchiveArray{
						pulumi.NewStringAsset("kubeProxyReplacement: true\n"),
					}
				},
			},
		})

		return err
	}))

	releases := m.of(releaseType)
	require.Len(t, releases, 1)

	// The whole point of the values-as-templates move: what reaches Helm is a
	// file, and a release that quietly carried none would install the chart on
	// its defaults with nothing said.
	assert.True(t, releases[0]["valueYamlFiles"].HasValue(),
		"the rendered values never reached the release")
}
