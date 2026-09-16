package main

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterref"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/hetzner"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/internals"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fixture is cluster.example.yaml — the file an operator copies — so these
// tests cannot drift from the shape the repository actually ships.
const exampleTopologyPath = "cluster.example.yaml"

// Sentinels for the values the cluster component produces. Distinct strings on
// purpose: the defect these tests exist for is a right name carrying another
// output's value, and identical placeholders would hide exactly that.
const (
	testKubeconfig        = "kubeconfig-sentinel"
	testTalosconfig       = "talosconfig-sentinel"
	testEndpoint          = "https://endpoint-sentinel:6443"
	testAPILoadBalancerIP = "203.0.113.200"
	testPodCIDR           = "10.244.0.0/16"
	testServiceCIDR       = "10.96.0.0/12"
	testNetworkID         = 4242
	testToken             = "token-sentinel"
)

func exampleTopology(t *testing.T) *hetzner.Topology {
	t.Helper()

	topology, err := hetzner.LoadTopology(exampleTopologyPath)
	require.NoError(t, err, "the committed example must load and validate")

	return topology
}

// fakeCluster stands in for what hetzner.NewCluster returns. The component
// itself is tested in internal/pkg/hetzner; what is under test here is which of its
// outputs reaches which name in the contract.
func fakeCluster() *hetzner.Cluster {
	return &hetzner.Cluster{
		Kubeconfig:        pulumi.String(testKubeconfig).ToStringOutput(),
		Talosconfig:       pulumi.String(testTalosconfig).ToStringOutput(),
		Endpoint:          pulumi.String(testEndpoint).ToStringOutput(),
		APILoadBalancerIP: pulumi.String(testAPILoadBalancerIP).ToStringOutput(),
		NetworkID:         pulumi.Int(testNetworkID).ToIntOutput(),
		PodCIDR:           pulumi.String(testPodCIDR).ToStringOutput(),
		ServiceCIDR:       pulumi.String(testServiceCIDR).ToStringOutput(),
	}
}

// resolve awaits one exported value. Outside a Pulumi run, which these
// outputs do not need: none of them depends on a resource.
func resolve(t *testing.T, value pulumi.Input) any {
	t.Helper()

	result, err := internals.UnsafeAwaitOutput(t.Context(), pulumi.ToOutput(value))
	require.NoError(t, err)

	return result.Value
}

// TestExports_CoverEveryDeclaredOutput is the contract check that used to be a
// text search in internal/pkg/clusterref: it could see the names this file mentions,
// and nothing more.
func TestExports_CoverEveryDeclaredOutput(t *testing.T) {
	t.Parallel()

	published := exports(exampleTopology(t), fakeCluster(), pulumi.String(""))

	names := make([]string, 0, len(published))
	for name := range published {
		names = append(names, name)
	}

	declared := append([]string(nil), clusterref.Declared...)

	sort.Strings(names)
	sort.Strings(declared)

	// Equality both ways: a declared output this tier does not publish fails a
	// layer at apply, and one published but not declared is a value no
	// consumer can read.
	assert.Equal(t, declared, names)
}

// TestExports_CarryTheValueEachNamePromises is the half the text search could
// not do. Every one of these is a string or an int, so podCidr wired to
// nodeSubnet compiles, exports, and breaks the layer that trusts it.
func TestExports_CarryTheValueEachNamePromises(t *testing.T) {
	t.Parallel()

	topology := exampleTopology(t)
	published := exports(topology, fakeCluster(), pulumi.String(testToken))

	for name, want := range map[string]any{
		clusterref.OutputContractVersion:   clusterref.ContractVersion,
		clusterref.OutputKubeconfig:        testKubeconfig,
		clusterref.OutputTalosconfig:       testTalosconfig,
		clusterref.OutputEndpoint:          testEndpoint,
		clusterref.OutputAPILoadBalancerIP: testAPILoadBalancerIP,
		clusterref.OutputNetworkID:         testNetworkID,
		clusterref.OutputPodCIDR:           testPodCIDR,
		clusterref.OutputServiceCIDR:       testServiceCIDR,
		clusterref.OutputHcloudToken:       testToken,

		// From the topology rather than from the component.
		clusterref.OutputNodeSubnet:        hetzner.DefaultNodeSubnet,
		clusterref.OutputClusterName:       topology.Metadata.Name,
		clusterref.OutputLocation:          topology.Placement.Location,
		clusterref.OutputControlPlaneCount: topology.ControlPlane.Count,
	} {
		require.Contains(t, published, name)
		assert.Equal(t, want, resolve(t, published[name]), "%s carries another output's value", name)
	}
}

// noResources is a mock monitor for the paths that create nothing: both of
// them return before hetzner.NewCluster is reached.
type noResources struct{}

func (noResources) NewResource(pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	return "", resource.PropertyMap{}, nil
}

func (noResources) Call(pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

func TestProgram_NamesTheTopologyFileItCouldNotFind(t *testing.T) {
	t.Setenv("PULUMI_CONFIG", "{}")

	const stack = "no-such-stack"

	err := pulumi.RunErr(program, pulumi.WithMocks("hetzner-cluster", stack, noResources{}))

	require.Error(t, err)
	// The remedy names the file to create. Reported by os.IsNotExist before,
	// which does not walk a %w chain — so this branch never ran and the
	// operator got the raw "open cluster.<stack>.yaml" instead.
	assert.Contains(t, err.Error(), topologyPath(stack))
	assert.Contains(t, err.Error(), "create")
}

// withConfig runs f as a Pulumi program with stack config set from cfg.
func withConfig(t *testing.T, cfg map[string]string, f func(*pulumi.Context) error) {
	t.Helper()

	raw, err := json.Marshal(cfg)
	require.NoError(t, err)

	t.Setenv("PULUMI_CONFIG", string(raw))

	require.NoError(t, pulumi.RunErr(f, pulumi.WithMocks("hetzner-cluster", "test", noResources{})))
}

// TestClusterToken_IsExportedEmptyWhenStackConfigHasNone pins the total
// contract clusterToken documents. Absent instead of empty, every consumer
// would have to handle the output's own absence, which is what the version
// gate exists to abolish.
//
// The warning that goes with it is not asserted here: a Pulumi context does
// not capture diagnostics. internal/pkg/pulumilog tests the line it produces.
func TestClusterToken_IsExportedEmptyWhenStackConfigHasNone(t *testing.T) {
	withConfig(t, map[string]string{}, func(ctx *pulumi.Context) error {
		token := clusterToken(ctx)

		assert.Equal(t, "", resolve(t, token))
		assert.False(t, pulumi.IsSecret(token), "there is no secret to mark")

		return nil
	})
}

func TestClusterToken_IsASecretWhenStackConfigHasOne(t *testing.T) {
	withConfig(t, map[string]string{hetzner.TokenConfigKey: testToken}, func(ctx *pulumi.Context) error {
		token := clusterToken(ctx)

		assert.Equal(t, testToken, resolve(t, token))
		// A token that reaches a layer unmarked is a credential Pulumi will
		// print in a diff.
		assert.True(t, pulumi.IsSecret(token))

		return nil
	})
}

// TestReport_NarratesEveryDecisionTheTopologyMakes is the list, held so a new
// switch in the topology cannot arrive unreported.
//
// This tier used to narrate nothing while every layer narrated its own
// choices. Pulumi prints the resources, so what was missing was never the
// actions — it was the handful of decisions derived from the topology, which
// are the ones that produce a cluster that comes up and then puzzles somebody.
func TestReport_NarratesEveryDecisionTheTopologyMakes(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("main.go")
	require.NoError(t, err)

	body := string(raw)

	start := strings.Index(body, "func report(")
	require.Positive(t, start, "report() is gone, and with it every decision this tier reports")

	end := strings.Index(body[start:], "\n}\n")
	require.Positive(t, end, "report() has no end")

	reported := body[start : start+end]

	// One line per decision a reader of the output has to be able to see.
	for component, why := range map[string]string{
		"control-plane": "how many nodes of what type, and where",
		"api":           "whether a load balancer fronts the API, or one node is its own endpoint",
		"scheduling":    "whether workloads may run on the control plane, which zero workers decides",
		"talos":         "which version and architecture, and what selected the image",
		"addressing":    "publicIPv4 off, which stops talosctl reaching a node from outside",
	} {
		assert.Contains(t, reported, `"`+component+`"`,
			"report() no longer mentions %q: %s", component, why)
	}

	// Through the predicates rather than by recomputing: a report that
	// derives a decision its own way can describe a cluster nobody built.
	for _, predicate := range []string{"TotalWorkers()", "APILoadBalanced()", "PublicIPv4Enabled()"} {
		assert.Contains(t, reported, predicate,
			"report() does not use %s, so it can disagree with what NewCluster decided", predicate)
	}
}
