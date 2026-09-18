package layer_test

import (
	"errors"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/internals"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/layer"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/values"
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

	// The shape 10-node-platform has: three components behind the CNI.
	ordered, err := layer.OrderForTest(layer.Components{
		{Chart: "hcloud-csi", After: []string{"cilium"}},
		{Chart: "hcloud-ccm", After: []string{"cilium"}},
		{Chart: "cilium"},
		{Chart: "metrics-server", After: []string{"cilium"}},
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"cilium", "hcloud-ccm", "hcloud-csi", "metrics-server"}, chartKeys(ordered),
		"the dependency first, then the rest by chart key")
}

func TestOrder_IsDeterministic(t *testing.T) {
	t.Parallel()

	// Map iteration order in Go is randomised, so an ordering that walked a
	// map would produce a different sequence per run — and every preview would
	// show a diff that means nothing. Ties break by chart key.
	first, err := layer.OrderForTest(layer.Components{
		{Chart: "metrics-server"}, {Chart: "hcloud-ccm"}, {Chart: "hcloud-csi"},
	})
	require.NoError(t, err)

	for range 20 {
		again, againErr := layer.OrderForTest(layer.Components{
			{Chart: "metrics-server"}, {Chart: "hcloud-ccm"}, {Chart: "hcloud-csi"},
		})
		require.NoError(t, againErr)
		require.Equal(t, chartKeys(first), chartKeys(again))
	}

	assert.Equal(t, []string{"hcloud-ccm", "hcloud-csi", "metrics-server"}, chartKeys(first))
}

func TestOrder_RefusesACycle(t *testing.T) {
	t.Parallel()

	// A cycle cannot be deployed in any order, and left to Pulumi it surfaces
	// as a resource graph error naming URNs rather than charts.
	_, err := layer.OrderForTest(layer.Components{
		{Chart: "hcloud-ccm", After: []string{"hcloud-csi"}},
		{Chart: "hcloud-csi", After: []string{"hcloud-ccm"}},
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
		{Chart: "hcloud-csi", After: []string{"cilium"}},
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
	_, err := layer.OrderForTest(layer.Components{{Chart: "cilium"}, {Chart: "cilium"}})

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
		Chart:  "cilium",
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
				ValuesFrom: func(*layer.Runner) pulumi.Output {
					return pulumi.All().ApplyT(func([]any) any {
						return values.Cilium{PodCIDR: "10.244.0.0/16", RoutingMode: "native"}
					})
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

func TestDeploy_AFailedRenderStopsTheRun(t *testing.T) {
	// It used to log a warning and return nil, which installed the chart on
	// its own defaults — the outcome the templates exist to prevent.
	setStackRef(t, "acme/hetzner-cluster/prod")

	err := run(t, newMocks(), func(runner *layer.Runner) error {
		_, err := runner.Deploy(layer.Components{
			{
				Chart: "cilium",
				// The wrong data shape for this template: the field the
				// template reads is not on it.
				StaticValues: struct{ NotAField string }{NotAField: "x"},
			},
		})

		return err
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "values for cilium")
}

// TestDeploy_RendersTheTemplateEvenWithNoValues is the invariant the
// declarative fields buy.
//
// A chart component that named no values used to install on the chart's own
// defaults — expressible by leaving one field nil, and silent. Every chart in
// internal/pkg/charts has exactly one template, so there is no such thing here as a
// release with no values file.
func TestDeploy_RendersTheTemplateEvenWithNoValues(t *testing.T) {
	setStackRef(t, "acme/hetzner-cluster/prod")

	m := newMocks()

	require.NoError(t, run(t, m, func(runner *layer.Runner) error {
		_, err := runner.Deploy(layer.Components{{Chart: "external-secrets"}})

		return err
	}))

	releases := m.of(releaseType)
	require.Len(t, releases, 1)

	assert.True(t, releases[0]["valueYamlFiles"].HasValue(),
		"a component with no values fields still must render its template")
}

// TestOrder_RefusesBothValuesFields keeps the choice explicit: one template
// takes one kind of data, and picking silently would install whichever the
// implementation happened to prefer.
func TestOrder_RefusesBothValuesFields(t *testing.T) {
	setStackRef(t, "acme/hetzner-cluster/prod")

	err := run(t, newMocks(), func(runner *layer.Runner) error {
		_, err := runner.Deploy(layer.Components{
			{
				Chart:        "cilium",
				StaticValues: values.Cilium{},
				ValuesFrom: func(*layer.Runner) pulumi.Output {
					return pulumi.All()
				},
			},
		})

		return err
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "both StaticValues and ValuesFrom")
}

// TestMustRelease_NamesWhatWasNotDeployed replaces two hand-written error
// paths that said the same thing in two wordings.
func TestMustRelease_NamesWhatWasNotDeployed(t *testing.T) {
	t.Parallel()

	_, err := layer.Deployed{}.MustRelease("argo-cd")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "argo-cd")
	assert.Contains(t, err.Error(), "not deployed")
}

// TestMustRelease_ReturnsTheReleaseWhenItIsThere is the other half, and it
// also pins that a Create component's resource is not mistaken for a release.
func TestMustRelease_ReturnsTheReleaseWhenItIsThere(t *testing.T) {
	setStackRef(t, "acme/hetzner-cluster/prod")

	m := newMocks()

	require.NoError(t, run(t, m, func(runner *layer.Runner) error {
		deployed, err := runner.Deploy(layer.Components{{Chart: "external-secrets"}})
		if err != nil {
			return err
		}

		release, err := deployed.MustRelease("external-secrets")
		require.NoError(t, err)
		require.NotNil(t, release)

		_, notARelease := deployed.MustRelease("cert-manager")
		assert.Error(t, notARelease, "a component that is not in the set must not resolve")

		return nil
	}))
}

// TestDeploy_AChartComponentMayDecline is the whole point of When: a chart
// that a stack has not asked for installs nothing, and nothing else in the set
// has to know.
func TestDeploy_AChartComponentMayDecline(t *testing.T) {
	setStackRef(t, "acme/hetzner-cluster/prod")

	m := newMocks()

	require.NoError(t, run(t, m, func(runner *layer.Runner) error {
		deployed, err := runner.Deploy(layer.Components{
			{
				Chart: "cilium",
				When:  func(*layer.Runner) (bool, error) { return false, nil },
			},
		})
		if err != nil {
			return err
		}

		// Absent from the map rather than nil in it, so a later DependsOn
		// cannot be handed a nil resource.
		_, found := deployed.Release("cilium")
		assert.False(t, found, "a component that declined is in Deployed")

		return nil
	}))

	assert.Empty(t, m.of(releaseType), "a component that declined installed a release anyway")
}

// TestDeploy_AComponentAfterOneThatDeclinedIsStillCreated holds the part that
// would be easy to get wrong: declining is not failing, and the rest of the
// layer goes on.
func TestDeploy_AComponentAfterOneThatDeclinedIsStillCreated(t *testing.T) {
	setStackRef(t, "acme/hetzner-cluster/prod")

	m := newMocks()

	require.NoError(t, run(t, m, func(runner *layer.Runner) error {
		_, err := runner.Deploy(layer.Components{
			{
				Chart: "cilium",
				When:  func(*layer.Runner) (bool, error) { return false, nil },
			},
			{
				Chart: "cert-manager",
				After: []string{"cilium"},
			},
		})

		return err
	}))

	assert.Len(t, m.of(releaseType), 1, "the component after the one that declined did not run")
}

// TestDeploy_AWhenThatCannotAnswerStopsTheRun is why When returns an error.
//
// The answer comes from a string an operator typed. Reading `yes` as false
// gives a cluster with no KEDA and an operator who believes otherwise, so the
// layer refuses rather than choosing one of the two states for them.
func TestDeploy_AWhenThatCannotAnswerStopsTheRun(t *testing.T) {
	setStackRef(t, "acme/hetzner-cluster/prod")

	err := run(t, newMocks(), func(runner *layer.Runner) error {
		_, deployErr := runner.Deploy(layer.Components{
			{
				Chart: "cilium",
				When: func(*layer.Runner) (bool, error) {
					return false, errors.New(`config "x" is "yes", which is not a boolean`)
				},
			},
		})

		return deployErr
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cilium:", "the failure does not name the component it came from")
	assert.Contains(t, err.Error(), "not a boolean")
}

// TestDeploy_WhenIsAskedBeforeTheValuesAreRendered keeps the cheap answer
// first: a declined component should not need its template to be renderable,
// which is what makes When usable for a chart whose values come from config
// that is only set when it is enabled.
func TestDeploy_WhenIsAskedBeforeTheValuesAreRendered(t *testing.T) {
	setStackRef(t, "acme/hetzner-cluster/prod")

	require.NoError(t, run(t, newMocks(), func(runner *layer.Runner) error {
		_, err := runner.Deploy(layer.Components{
			{
				Chart: "cilium",
				When:  func(*layer.Runner) (bool, error) { return false, nil },
				// The wrong data shape for this template: rendering it is an
				// error, and TestDeploy_AFailedRenderStopsTheRun proves that.
				StaticValues: struct{ NotAField string }{NotAField: "x"},
			},
		})

		return err
	}))
}

// Two groups, as a merged layer has them.
var (
	testServices = layer.Group{Type: "hetzner-iac:test:Services", Name: "services"}
	testIngress  = layer.Group{Type: "hetzner-iac:test:Ingress", Name: "ingress"}
)

// TestDeploy_AGroupPutsItsTypeInEveryChildURN is the property the whole
// mechanism exists for.
//
// Two former layers in one project are only separable if the STATE says which
// is which. The group's type is what carries that, because it lands in every
// child's URN — and a URN is what `--target '**:Ingress$**'` matches. Asserted
// on the URN itself rather than on the mock, because the URN is the thing the
// CLI will be given.
func TestDeploy_AGroupPutsItsTypeInEveryChildURN(t *testing.T) {
	setStackRef(t, "acme/hetzner-cluster/prod")

	require.NoError(t, run(t, newMocks(), func(runner *layer.Runner) error {
		deployed, err := runner.Deploy(layer.Components{
			{Group: testServices, Chart: "cert-manager"},
			{Group: testIngress, Chart: "external-secrets"},
		})
		require.NoError(t, err)

		for chart, group := range map[string]layer.Group{
			"cert-manager":     testServices,
			"external-secrets": testIngress,
		} {
			release, ok := deployed.Release(chart)
			require.True(t, ok, chart)

			urn, awaitErr := internals.UnsafeAwaitOutput(runner.Ctx.Context(), release.URN())
			require.NoError(t, awaitErr, chart)

			assert.Contains(t, urn.Value, group.Type,
				"%s is not under %s, so its group is invisible in the state and "+
					"--target cannot select it", chart, group.Type)
		}

		return nil
	}))
}

// TestDeploy_TwoGroupsAreToldApartByTheirURNs is the same property from the
// angle that matters: not just present, but DISTINCT. One shared type would
// put both sets under one name and leave them indistinguishable, which is the
// outcome the merge must not have.
func TestDeploy_TwoGroupsAreToldApartByTheirURNs(t *testing.T) {
	setStackRef(t, "acme/hetzner-cluster/prod")

	require.NoError(t, run(t, newMocks(), func(runner *layer.Runner) error {
		deployed, err := runner.Deploy(layer.Components{
			{Group: testServices, Chart: "cert-manager"},
			{Group: testIngress, Chart: "external-secrets"},
		})
		require.NoError(t, err)

		services, _ := deployed.Release("cert-manager")
		ingress, _ := deployed.Release("external-secrets")

		one, err := internals.UnsafeAwaitOutput(runner.Ctx.Context(), services.URN())
		require.NoError(t, err)
		other, err := internals.UnsafeAwaitOutput(runner.Ctx.Context(), ingress.URN())
		require.NoError(t, err)

		assert.NotContains(t, one.Value, testIngress.Type)
		assert.NotContains(t, other.Value, testServices.Type)

		return nil
	}))
}

// TestDeploy_AnUngroupedComponentStaysWhereItWas keeps the field optional: a
// layer that was never merged with another must look exactly as it did, or
// every other layer's state moves for nothing.
func TestDeploy_AnUngroupedComponentStaysWhereItWas(t *testing.T) {
	setStackRef(t, "acme/hetzner-cluster/prod")

	require.NoError(t, run(t, newMocks(), func(runner *layer.Runner) error {
		deployed, err := runner.Deploy(layer.Components{{Chart: "cert-manager"}})
		require.NoError(t, err)

		release, ok := deployed.Release("cert-manager")
		require.True(t, ok)

		urn, awaitErr := internals.UnsafeAwaitOutput(runner.Ctx.Context(), release.URN())
		require.NoError(t, awaitErr)

		// Straight under the stack: one `$` separator would mean a parent.
		assert.NotContains(t, urn.Value, "$",
			"an ungrouped component gained a parent, which moves its URN for nothing")

		return nil
	}))
}

// TestOrder_RefusesADependencyAcrossGroups is the invariant that keeps two
// groups independently appliable.
//
// An After becomes a DependsOn. Across a group boundary that means
// `destroy --target` on one group refuses without --target-dependents, and
// takes the other group's resource with it when given one — so the merge would
// have produced one inseparable thing wearing two names.
func TestOrder_RefusesADependencyAcrossGroups(t *testing.T) {
	t.Parallel()

	_, err := layer.OrderForTest(layer.Components{
		{Group: testServices, Chart: "cert-manager"},
		{Group: testIngress, Chart: "traefik", After: []string{"cert-manager"}},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "across groups")
	assert.Contains(t, err.Error(), "traefik")
	assert.Contains(t, err.Error(), "cert-manager")
}

// TestOrder_AllowsADependencyInsideAGroup is the other half: grouping must not
// break the ordering a layer genuinely needs.
func TestOrder_AllowsADependencyInsideAGroup(t *testing.T) {
	t.Parallel()

	ordered, err := layer.OrderForTest(layer.Components{
		{Group: testServices, Chart: "metrics-server", After: []string{"cert-manager"}},
		{Group: testServices, Chart: "cert-manager"},
	})

	require.NoError(t, err)
	require.Len(t, ordered, 2)
	assert.Equal(t, "cert-manager", ordered[0].Key())
}
