package main

import (
	"sync"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/hetzner"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/layer"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testProject     = "backup"
	testStack       = "dev"
	testClusterName = "platform-dev"
	testToken       = "backup-project-token" // #nosec G101 -- a fixture, not a credential

	getServersToken = "hcloud:index/getServers:getServers"
)

// withConfig sets this project's stack config for a mocked run.
func withConfig(values map[string]string) pulumi.RunOption {
	return func(info *pulumi.RunInfo) {
		info.Config = values
	}
}

// serverMocks answers getServers with the servers it holds, and records what
// each invoke asked for and through which provider.
type serverMocks struct {
	mu        sync.Mutex
	servers   []string
	selectors []string
	providers []string
}

func (*serverMocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	return args.Name, args.Inputs, nil
}

func (m *serverMocks) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	if args.Token != getServersToken {
		return resource.PropertyMap{}, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if selector := args.Args["withSelector"]; selector.IsString() {
		m.selectors = append(m.selectors, selector.StringValue())
	}

	m.providers = append(m.providers, args.Provider)

	servers := make([]resource.PropertyValue, 0, len(m.servers))
	for _, name := range m.servers {
		servers = append(servers, resource.NewObjectProperty(resource.PropertyMap{
			"name": resource.NewStringProperty(name),
		}))
	}

	return resource.PropertyMap{"servers": resource.NewArrayProperty(servers)}, nil
}

// runSeparateProject runs the check against mocks that can see servers, and
// returns its result.
func runSeparateProject(t *testing.T, mocks *serverMocks) (bool, error) {
	t.Helper()

	resolved := make(chan bool, 1)

	runErr := pulumi.RunErr(func(ctx *pulumi.Context) error {
		provider, err := hetzner.NewProvider(ctx, pulumi.String(testToken))
		if err != nil {
			return err
		}

		check := separateProject(ctx, provider, pulumi.String(testClusterName).ToStringOutput())

		// Exported, as deploy exports it: that is what makes the engine await
		// the check, and so what makes a refusal fail the run.
		ctx.Export(OutputSeparateProject, check)

		check.ApplyT(func(separate bool) bool {
			resolved <- separate

			return separate
		})

		return nil
	}, pulumi.WithMocks(testProject, testStack, mocks))

	// A refusal never reaches the callback; the run returns its error.
	if runErr != nil {
		return false, runErr
	}

	return <-resolved, nil
}

// TestSeparateProject_PassesWhenTheClusterIsInvisible: a token from another
// project sees none of the cluster's servers.
func TestSeparateProject_PassesWhenTheClusterIsInvisible(t *testing.T) {
	t.Parallel()

	mocks := &serverMocks{}

	separate, err := runSeparateProject(t, mocks)
	require.NoError(t, err)
	assert.True(t, separate)
}

// TestSeparateProject_RefusesTheClustersProject: a token that sees the
// cluster's servers is a token whose project holds the cluster, whatever its
// string is.
func TestSeparateProject_RefusesTheClustersProject(t *testing.T) {
	t.Parallel()

	mocks := &serverMocks{servers: []string{testClusterName + "-control-plane-0"}}

	_, err := runSeparateProject(t, mocks)
	require.Error(t, err)
	assert.Contains(t, err.Error(), testClusterName+"-control-plane-0")
	assert.Contains(t, err.Error(), TokenTask)
}

// TestSeparateProject_AsksForThisClusterThroughTheBackupProvider: the question
// is about this cluster, and it has to be asked with the backup tier's token —
// through the default provider it would ask nothing useful.
func TestSeparateProject_AsksForThisClusterThroughTheBackupProvider(t *testing.T) {
	t.Parallel()

	mocks := &serverMocks{}

	_, err := runSeparateProject(t, mocks)
	require.NoError(t, err)

	mocks.mu.Lock()
	defer mocks.mu.Unlock()

	assert.Equal(t, []string{clusterspec.ClusterSelector(testClusterName)}, mocks.selectors)
	require.Len(t, mocks.providers, 1)
	assert.Contains(t, mocks.providers[0], "::"+hetzner.ProviderName+"::",
		"the servers were listed through another provider, so the check says nothing about "+
			"the backup tier's token")
}

func TestRefuseSharedProject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		visible []string
		refused bool
	}{
		{name: "nothing visible", visible: nil, refused: false},
		{name: "empty list", visible: []string{}, refused: false},
		{name: "one server", visible: []string{"platform-dev-control-plane-0"}, refused: true},
		{
			name:    "several servers",
			visible: []string{"platform-dev-control-plane-0", "platform-dev-worker-0"},
			refused: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := refuseSharedProject(testClusterName, test.visible)
			if !test.refused {
				assert.NoError(t, err)

				return
			}

			require.Error(t, err)

			for _, server := range test.visible {
				assert.Contains(t, err.Error(), server, "the refusal does not name what it saw")
			}
		})
	}
}

// readOwnToken runs ownToken against the given stack config.
func readOwnToken(t *testing.T, values map[string]string) (string, error) {
	t.Helper()

	resolved := make(chan string, 1)

	runErr := pulumi.RunErr(func(ctx *pulumi.Context) error {
		token, err := ownToken(config.New(ctx, ""))
		if err != nil {
			return err
		}

		token.ApplyT(func(value string) string {
			resolved <- value

			return value
		})

		return nil
	}, pulumi.WithMocks(testProject, testStack, &serverMocks{}), withConfig(values))

	// A refusal fails the run before anything is resolved.
	if runErr != nil {
		return "", runErr
	}

	return <-resolved, nil
}

// TestOwnToken_IsRequired: with no token of its own the tier refuses, naming
// the task, rather than falling back to the cluster's — the fallback is how it
// came to share the cluster's project.
func TestOwnToken_IsRequired(t *testing.T) {
	t.Parallel()

	for name, values := range map[string]map[string]string{
		"unset": {},
		"empty": {testProject + ":" + layer.HcloudTokenKey: ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := readOwnToken(t, values)
			require.Error(t, err)
			assert.Contains(t, err.Error(), TokenTask)
		})
	}
}

// TestOwnToken_IsReadFromThisTiersConfig: the value is the one configured
// under this project's own key.
func TestOwnToken_IsReadFromThisTiersConfig(t *testing.T) {
	t.Parallel()

	token, err := readOwnToken(t, map[string]string{testProject + ":" + layer.HcloudTokenKey: testToken})
	require.NoError(t, err)
	assert.Equal(t, testToken, token)
}
