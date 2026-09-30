package main

import (
	"encoding/json"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterref"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/layer"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testProject = "ingress"
	testStack   = "test"
)

// clusterTier stands in for the cluster tier's stack. token == "" is a cluster
// built from an environment token rather than from stack config, which the
// tier exports as an empty string.
type clusterTier struct{ token string }

func (m clusterTier) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	if args.TypeToken != "pulumi:pulumi:StackReference" {
		// Numeric, because hcloud ids are and the load balancer's services
		// parse it: a name here fails for a reason no real apply has.
		return "1", args.Inputs, nil
	}

	// The whole declared set: the contract is total, and the tier exports an
	// empty token rather than none when it has none.
	return args.Name, resource.PropertyMap{"outputs": resource.NewObjectProperty(resource.PropertyMap{
		resource.PropertyKey(clusterref.OutputContractVersion):   resource.NewNumberProperty(clusterref.ContractVersion),
		resource.PropertyKey(clusterref.OutputKubeconfig):        resource.NewStringProperty("apiVersion: v1"),
		resource.PropertyKey(clusterref.OutputTalosconfig):       resource.NewStringProperty("context: test"),
		resource.PropertyKey(clusterref.OutputEndpoint):          resource.NewStringProperty("https://203.0.113.200:6443"),
		resource.PropertyKey(clusterref.OutputAPILoadBalancerIP): resource.NewStringProperty(""),
		resource.PropertyKey(clusterref.OutputNetworkID):         resource.NewNumberProperty(12637895),
		resource.PropertyKey(clusterref.OutputNodeSubnet):        resource.NewStringProperty(testNodeSubnet),
		resource.PropertyKey(clusterref.OutputPodCIDR):           resource.NewStringProperty("10.244.0.0/16"),
		resource.PropertyKey(clusterref.OutputServiceCIDR):       resource.NewStringProperty("10.96.0.0/12"),
		resource.PropertyKey(clusterref.OutputClusterName):       resource.NewStringProperty("platform-test"),
		resource.PropertyKey(clusterref.OutputLocation):          resource.NewStringProperty(clusterref.ProbeLocation),
		resource.PropertyKey(clusterref.OutputHcloudToken):       resource.NewStringProperty(m.token),
		resource.PropertyKey(clusterref.OutputControlPlaneCount): resource.NewNumberProperty(3),
		resource.PropertyKey(clusterref.OutputRoutingMode):       resource.NewStringProperty(clusterspec.RoutingModeNative),
		resource.PropertyKey(clusterref.OutputDomain):            resource.NewStringProperty(""),
		resource.PropertyKey(clusterref.OutputDNSZone):           resource.NewStringProperty(""),
	})}, nil
}

func (clusterTier) Call(pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

// deployAgainst runs this layer against a cluster tier exporting token.
//
// Config reaches a Pulumi program through PULUMI_CONFIG, and t.Setenv forbids
// t.Parallel, so these tests run in sequence.
func deployAgainst(t *testing.T, token string) error {
	t.Helper()

	raw, err := json.Marshal(map[string]string{testProject + ":clusterStackRef": "acme/hetzner-cluster/test"})
	require.NoError(t, err)
	t.Setenv("PULUMI_CONFIG", string(raw))

	return pulumi.RunErr(func(ctx *pulumi.Context) error {
		runner, err := layer.New(ctx)
		if err != nil {
			return err
		}

		return deploy(runner)
	}, pulumi.WithMocks(testProject, testStack, clusterTier{token: token}))
}

// TestDeploy_RefusesAnEmptyClusterToken is the provider that authenticated as
// nothing.
//
// The tier exports an empty token when its own was never set in stack config,
// and clusterref documents that as a real state. Built from it, the hcloud
// provider fails at the first call with a 401 that names no token at all —
// 10-node-platform already refuses the same export and says what to set.
func TestDeploy_RefusesAnEmptyClusterToken(t *testing.T) {
	err := deployAgainst(t, "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), clusterref.OutputHcloudToken)
	assert.Contains(t, err.Error(), "config set --secret "+testProject+":hcloudToken",
		"the refusal names this layer's own override")
}

func TestDeploy_TakesTheClusterTiersToken(t *testing.T) {
	require.NoError(t, deployAgainst(t, "token-from-the-cluster-tier"))
}
