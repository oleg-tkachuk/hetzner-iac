package layer_test

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/charts"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterref"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/layer"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testProject = "test-layer"
	testStack   = "test"
	releaseType = "kubernetes:helm.sh/v3:Release"
)

// mocks records registered resources and answers the StackReference the layer
// resolves the cluster through.
type mocks struct {
	mu        sync.Mutex
	resources map[string][]resource.PropertyMap
}

func newMocks() *mocks {
	return &mocks{resources: map[string][]resource.PropertyMap{}}
}

func (m *mocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	m.mu.Lock()
	m.resources[args.TypeToken] = append(m.resources[args.TypeToken], args.Inputs)
	m.mu.Unlock()

	if args.TypeToken == "pulumi:pulumi:StackReference" {
		// The whole declared set, version included. A partial one is not a
		// shape the tier can produce: internal/pkg/clusterref gates every output on
		// the version, so a mock missing it fails every test here with the
		// stale-producer message — which is how that gate was verified.
		return args.Name, resource.PropertyMap{
			"outputs": resource.NewObjectProperty(resource.PropertyMap{
				resource.PropertyKey(clusterref.OutputContractVersion):   resource.NewNumberProperty(clusterref.ContractVersion),
				resource.PropertyKey(clusterref.OutputKubeconfig):        resource.NewStringProperty("apiVersion: v1"),
				resource.PropertyKey(clusterref.OutputTalosconfig):       resource.NewStringProperty("context: test"),
				resource.PropertyKey(clusterref.OutputEndpoint):          resource.NewStringProperty("https://203.0.113.200:6443"),
				resource.PropertyKey(clusterref.OutputAPILoadBalancerIP): resource.NewStringProperty(""),
				resource.PropertyKey(clusterref.OutputNetworkID):         resource.NewNumberProperty(12637895),
				resource.PropertyKey(clusterref.OutputNodeSubnet):        resource.NewStringProperty("10.0.1.0/24"),
				resource.PropertyKey(clusterref.OutputPodCIDR):           resource.NewStringProperty("10.244.0.0/16"),
				resource.PropertyKey(clusterref.OutputServiceCIDR):       resource.NewStringProperty("10.96.0.0/12"),
				resource.PropertyKey(clusterref.OutputClusterName):       resource.NewStringProperty("platform-prod"),
				resource.PropertyKey(clusterref.OutputLocation):          resource.NewStringProperty(platform.ProbeLocation),
				resource.PropertyKey(clusterref.OutputHcloudToken):       resource.NewStringProperty("token"),
				resource.PropertyKey(clusterref.OutputControlPlaneCount): resource.NewNumberProperty(3),
			}),
		}, nil
	}

	return args.Name, args.Inputs, nil
}

func (m *mocks) Call(pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

func (m *mocks) of(token string) []resource.PropertyMap {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.resources[token]
}

// setStackRef points the layer at a cluster stack. Config reaches a Pulumi
// program through PULUMI_CONFIG, which is why these tests do not run in
// parallel — t.Setenv forbids it.
func setStackRef(t *testing.T, ref string) {
	t.Helper()

	raw, err := json.Marshal(map[string]string{testProject + ":clusterStackRef": ref})
	require.NoError(t, err)

	t.Setenv("PULUMI_CONFIG", string(raw))
}

func run(t *testing.T, m *mocks, fn func(*layer.Runner) error) error {
	t.Helper()

	return pulumi.RunErr(func(ctx *pulumi.Context) error {
		runner, err := layer.New(ctx)
		if err != nil {
			return err
		}

		return fn(runner)
	}, pulumi.WithMocks(testProject, testStack, m))
}

func TestNew_RequiresAClusterStackRef(t *testing.T) {
	t.Setenv("PULUMI_CONFIG", "{}")

	// Without this the layer would build a provider from an empty kubeconfig
	// and fail much later, against an unclear target.
	err := run(t, newMocks(), func(*layer.Runner) error { return nil })

	require.Error(t, err)
	assert.Contains(t, err.Error(), "clusterStackRef")
	assert.Contains(t, err.Error(), "task platform:init",
		"the remedy is the task that derives the reference, not a hand-typed one")
}

func TestNew_BuildsAProviderFromTheClusterKubeconfig(t *testing.T) {
	setStackRef(t, "acme/hetzner-cluster/prod")

	m := newMocks()

	require.NoError(t, run(t, m, func(runner *layer.Runner) error {
		assert.NotNil(t, runner.Provider)
		assert.NotNil(t, runner.Cluster)

		return nil
	}))

	providers := m.of("pulumi:providers:kubernetes")
	require.Len(t, providers, 1)

	// Server-side apply is what makes a re-run converge on a resource another
	// controller also writes to, instead of silently overwriting it.
	assert.True(t, providers[0]["enableServerSideApply"].BoolValue())

	// The provider's identity, stated rather than guessed. Without it the
	// provider decides for itself whether a configuration change is an update
	// or a replacement, and a replacement takes every release and every
	// secret in the layer with it.
	assert.Equal(t, "platform-prod", providers[0]["clusterIdentifier"].StringValue(),
		"the provider must be identified by the cluster it targets")

	// An unreachable cluster must fail the operation, not empty the state.
	assert.False(t, providers[0]["deleteUnreachable"].BoolValue())
}

func TestWith_DoesNotMutateTheSharedOptions(t *testing.T) {
	setStackRef(t, "acme/hetzner-cluster/prod")

	require.NoError(t, run(t, newMocks(), func(runner *layer.Runner) error {
		before := len(runner.Options)

		runner.With(pulumi.DeleteBeforeReplace(true))
		runner.With(pulumi.Protect(true))

		// Appending to the shared slice would leak one component's options
		// into every component created after it — a DependsOn that silently
		// becomes global.
		assert.Len(t, runner.Options, before)

		return nil
	}))
}

func TestRelease_UsesThePinnedVersionFromTheRegistry(t *testing.T) {
	setStackRef(t, "acme/hetzner-cluster/prod")

	m := newMocks()

	require.NoError(t, run(t, m, func(runner *layer.Runner) error {
		_, err := runner.Release(layer.ReleaseArgs{Chart: "cilium"})

		return err
	}))

	releases := m.of(releaseType)
	require.Len(t, releases, 1)

	cilium := charts.MustGet("cilium")
	assert.Equal(t, cilium.Name, releases[0]["chart"].StringValue())
	assert.Equal(t, cilium.Version, releases[0]["version"].StringValue())
	assert.Equal(t, cilium.Namespace, releases[0]["namespace"].StringValue())
	assert.Equal(t, cilium.Repo, releases[0]["repositoryOpts"].ObjectValue()["repo"].StringValue())
}

func TestRelease_IsAtomicAndWaitsForHookJobs(t *testing.T) {
	setStackRef(t, "acme/hetzner-cluster/prod")

	m := newMocks()

	require.NoError(t, run(t, m, func(runner *layer.Runner) error {
		_, err := runner.Release(layer.ReleaseArgs{Chart: "cert-manager"})

		return err
	}))

	release := m.of(releaseType)[0]

	// Atomic: a failed upgrade rolls back instead of leaving half a release
	// for the next run to inherit — which is what makes re-running converge.
	assert.True(t, release["atomic"].BoolValue())
	// WaitForJobs: cert-manager runs startupapicheck in a hook Job. Without
	// waiting, the release reports ready while the check has not run.
	assert.True(t, release["waitForJobs"].BoolValue())
	// Helm keeps release history forever by default, which on a repeatedly
	// reconciled platform becomes thousands of secrets.
	assert.EqualValues(t, 5, release["maxHistory"].NumberValue())
}

func TestRelease_RejectsAnUnpinnedChart(t *testing.T) {
	setStackRef(t, "acme/hetzner-cluster/prod")

	// A layer can only name a registry key, so there is no spelling of
	// Release that installs a chart nobody pinned.
	err := run(t, newMocks(), func(runner *layer.Runner) error {
		_, err := runner.Release(layer.ReleaseArgs{Chart: "not-registered"})

		return err
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown chart")
}

func TestRelease_NameAndNamespaceOverrides(t *testing.T) {
	setStackRef(t, "acme/hetzner-cluster/prod")

	m := newMocks()

	require.NoError(t, run(t, m, func(runner *layer.Runner) error {
		_, err := runner.Release(layer.ReleaseArgs{
			Chart:     "cert-manager",
			Name:      "cert-manager-primary",
			Namespace: "pki",
		})

		return err
	}))

	release := m.of(releaseType)[0]
	assert.Equal(t, "cert-manager-primary", release["name"].StringValue())
	assert.Equal(t, "pki", release["namespace"].StringValue())
	// The version still comes from the registry: overriding the name must not
	// be a way to float the version.
	assert.Equal(t, charts.MustGet("cert-manager").Version, release["version"].StringValue())
}

func TestRelease_DefaultsTheReleaseNameToTheRegistryKey(t *testing.T) {
	setStackRef(t, "acme/hetzner-cluster/prod")

	m := newMocks()

	require.NoError(t, run(t, m, func(runner *layer.Runner) error {
		_, err := runner.Release(layer.ReleaseArgs{Chart: "argo-cd"})

		return err
	}))

	// So that `helm list -A` reads like the layer list.
	assert.Equal(t, "argo-cd", m.of(releaseType)[0]["name"].StringValue())
}

func TestRelease_TimeoutOverride(t *testing.T) {
	setStackRef(t, "acme/hetzner-cluster/prod")

	m := newMocks()

	require.NoError(t, run(t, m, func(runner *layer.Runner) error {
		if _, err := runner.Release(layer.ReleaseArgs{Chart: "cilium"}); err != nil {
			return err
		}

		_, err := runner.Release(layer.ReleaseArgs{
			Chart:          "argo-cd",
			TimeoutSeconds: 1200,
		})

		return err
	}))

	releases := m.of(releaseType)
	require.Len(t, releases, 2)

	byName := map[string]resource.PropertyMap{}
	for _, release := range releases {
		byName[release["name"].StringValue()] = release
	}

	assert.EqualValues(t, 600, byName["cilium"]["timeout"].NumberValue())
	assert.EqualValues(t, 1200, byName["argo-cd"]["timeout"].NumberValue())
}

func TestStringOr_FallsBackOnlyWhenUnset(t *testing.T) {
	setStackRef(t, "acme/hetzner-cluster/prod")

	require.NoError(t, run(t, newMocks(), func(r *layer.Runner) error {
		// Nothing set this key, so the default stands.
		assert.Equal(t, "30d", r.StringOr("metricsRetention", "30d"))

		return nil
	}))
}

func TestStringOr_PrefersTheConfiguredValue(t *testing.T) {
	raw, err := json.Marshal(map[string]string{
		testProject + ":clusterStackRef":  "acme/hetzner-cluster/prod",
		testProject + ":metricsRetention": "90d",
	})
	require.NoError(t, err)
	t.Setenv("PULUMI_CONFIG", string(raw))

	require.NoError(t, run(t, newMocks(), func(r *layer.Runner) error {
		assert.Equal(t, "90d", r.StringOr("metricsRetention", "30d"))

		return nil
	}))
}

func TestCfg_IsNamespacedToTheProjectWithoutNamingIt(t *testing.T) {
	// The point of holding the config on the Runner. Every layer used to write
	// `config.New(ctx, "gitops")` — its own project name as a literal, in a
	// second place. Renaming a project then leaves the layer reading config
	// nobody sets, which is not hypothetical: merging two layers renamed one
	// and the literal was missed.
	//
	// testProject is the project the mock runs under, so a key written with
	// that prefix must be visible without the layer ever spelling it.
	raw, err := json.Marshal(map[string]string{
		testProject + ":clusterStackRef": "acme/hetzner-cluster/prod",
		testProject + ":someKey":         "read-without-naming-the-project",
	})
	require.NoError(t, err)
	t.Setenv("PULUMI_CONFIG", string(raw))

	require.NoError(t, run(t, newMocks(), func(r *layer.Runner) error {
		assert.Equal(t, "read-without-naming-the-project", r.Cfg.Get("someKey"))

		return nil
	}))
}

// deployMocks records the dependency edges each release was registered with,
// out of the register RPC rather than out of the order the mock was called in.
//
// The distinction matters and was learned the hard way: an earlier test of the
// same kind recorded call order, passed, and kept passing with the DependsOn
// deleted, because with an instant mock the calls arrive in program order
// whether or not anything depends on anything.
type deployMocks struct {
	mu   sync.Mutex
	deps map[string][]string
}

func newDeployMocks() *deployMocks {
	return &deployMocks{deps: map[string][]string{}}
}

func (m *deployMocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	if args.TypeToken == "pulumi:pulumi:StackReference" {
		return newMocks().NewResource(args)
	}

	if args.RegisterRPC != nil {
		m.mu.Lock()
		m.deps[args.Name] = append(m.deps[args.Name], args.RegisterRPC.GetDependencies()...)
		m.mu.Unlock()
	}

	return args.Name, args.Inputs, nil
}

func (*deployMocks) Call(pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

func (m *deployMocks) dependsOn(name, other string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, urn := range m.deps[name] {
		if strings.Contains(urn, "::"+other) {
			return true
		}
	}

	return false
}

func TestDeploy_TurnsAfterIntoADependencyTheEngineHolds(t *testing.T) {
	setStackRef(t, "acme/hetzner-cluster/prod")

	m := newDeployMocks()

	require.NoError(t, pulumi.RunErr(func(ctx *pulumi.Context) error {
		runner, err := layer.New(ctx)
		if err != nil {
			return err
		}

		// Both charts whose template takes no data, because a component now
		// always renders one: cilium's needs an APIHost, and this test is
		// about ordering rather than about values.
		_, err = runner.Deploy(layer.Components{
			{Chart: "external-secrets", After: []string{"cert-manager"}},
			{Chart: "cert-manager"},
		})

		return err
	}, pulumi.WithMocks(testProject, testStack, m)))

	assert.True(t, m.dependsOn("external-secrets", "cert-manager"),
		"After must become a DependsOn, not merely an earlier call")
	assert.False(t, m.dependsOn("cert-manager", "external-secrets"),
		"the dependency must not run backwards")
}
