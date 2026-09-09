package layer_test

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/charts"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/clusterref"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/layer"
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
		return args.Name, resource.PropertyMap{
			"outputs": resource.NewObjectProperty(resource.PropertyMap{
				resource.PropertyKey(clusterref.OutputKubeconfig):  resource.NewStringProperty("apiVersion: v1"),
				resource.PropertyKey(clusterref.OutputClusterName): resource.NewStringProperty("platform-prod"),
				resource.PropertyKey(clusterref.OutputPodCIDR):     resource.NewStringProperty("10.244.0.0/16"),
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
		_, err := runner.Release(runner.Ctx, layer.ReleaseArgs{Chart: "cilium"})

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
		_, err := runner.Release(runner.Ctx, layer.ReleaseArgs{Chart: "ingress-nginx"})

		return err
	}))

	release := m.of(releaseType)[0]

	// Atomic: a failed upgrade rolls back instead of leaving half a release
	// for the next run to inherit — which is what makes re-running converge.
	assert.True(t, release["atomic"].BoolValue())
	// WaitForJobs: ingress-nginx generates its admission-webhook certificate
	// in a hook Job. Without waiting, the release reports ready while the
	// webhook still has no certificate.
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
		_, err := runner.Release(runner.Ctx, layer.ReleaseArgs{Chart: "not-registered"})

		return err
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown chart")
}

func TestRelease_NameAndNamespaceOverrides(t *testing.T) {
	setStackRef(t, "acme/hetzner-cluster/prod")

	m := newMocks()

	require.NoError(t, run(t, m, func(runner *layer.Runner) error {
		_, err := runner.Release(runner.Ctx, layer.ReleaseArgs{
			Chart:     "loki",
			Name:      "loki-primary",
			Namespace: "logging",
		})

		return err
	}))

	release := m.of(releaseType)[0]
	assert.Equal(t, "loki-primary", release["name"].StringValue())
	assert.Equal(t, "logging", release["namespace"].StringValue())
	// The version still comes from the registry: overriding the name must not
	// be a way to float the version.
	assert.Equal(t, charts.MustGet("loki").Version, release["version"].StringValue())
}

func TestRelease_DefaultsTheReleaseNameToTheRegistryKey(t *testing.T) {
	setStackRef(t, "acme/hetzner-cluster/prod")

	m := newMocks()

	require.NoError(t, run(t, m, func(runner *layer.Runner) error {
		_, err := runner.Release(runner.Ctx, layer.ReleaseArgs{Chart: "argo-cd"})

		return err
	}))

	// So that `helm list -A` reads like the layer list.
	assert.Equal(t, "argo-cd", m.of(releaseType)[0]["name"].StringValue())
}

func TestRelease_TimeoutOverride(t *testing.T) {
	setStackRef(t, "acme/hetzner-cluster/prod")

	m := newMocks()

	require.NoError(t, run(t, m, func(runner *layer.Runner) error {
		if _, err := runner.Release(runner.Ctx, layer.ReleaseArgs{Chart: "cilium"}); err != nil {
			return err
		}

		_, err := runner.Release(runner.Ctx, layer.ReleaseArgs{
			Chart:          "kube-prometheus-stack",
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
	assert.EqualValues(t, 1200, byName["kube-prometheus-stack"]["timeout"].NumberValue())
}
