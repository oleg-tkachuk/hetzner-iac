package clusterref_test

import (
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/clusterref"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stackMocks struct {
	outputs resource.PropertyMap
}

func (m stackMocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	if args.TypeToken == "pulumi:pulumi:StackReference" {
		return args.Name, resource.PropertyMap{
			"outputs": resource.NewObjectProperty(m.outputs),
		}, nil
	}

	return args.Name, args.Inputs, nil
}

func (stackMocks) Call(pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

func TestResolve_ReadsTheClusterTierOutputs(t *testing.T) {
	t.Parallel()

	mocks := stackMocks{outputs: resource.PropertyMap{
		resource.PropertyKey(clusterref.OutputKubeconfig):  resource.NewStringProperty("apiVersion: v1"),
		resource.PropertyKey(clusterref.OutputEndpoint):    resource.NewStringProperty("https://203.0.113.200:6443"),
		resource.PropertyKey(clusterref.OutputPodCIDR):     resource.NewStringProperty("10.244.0.0/16"),
		resource.PropertyKey(clusterref.OutputServiceCIDR): resource.NewStringProperty("10.96.0.0/12"),
		resource.PropertyKey(clusterref.OutputClusterName): resource.NewStringProperty("platform-prod"),
		resource.PropertyKey(clusterref.OutputLocation):    resource.NewStringProperty("hel1"),
	}}

	var (
		endpoint    string
		podCIDR     string
		clusterName string
	)

	done := make(chan struct{})

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		cluster, err := clusterref.Resolve(ctx, "acme/hetzner-cluster/prod")
		if err != nil {
			return err
		}

		pulumi.All(cluster.Endpoint, cluster.PodCIDR, cluster.ClusterName).
			ApplyT(func(values []any) error {
				endpoint, _ = values[0].(string)
				podCIDR, _ = values[1].(string)
				clusterName, _ = values[2].(string)

				close(done)

				return nil
			})

		return nil
	}, pulumi.WithMocks("hetzner-iac", "test", mocks))

	require.NoError(t, err)
	<-done

	assert.Equal(t, "https://203.0.113.200:6443", endpoint)
	assert.Equal(t, "10.244.0.0/16", podCIDR)
	assert.Equal(t, "platform-prod", clusterName)
}

func TestResolve_RejectsAnEmptyReference(t *testing.T) {
	t.Parallel()

	// An empty reference would otherwise surface as a StackReference to the
	// stack named "", which fails much later with an unhelpful message.
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := clusterref.Resolve(ctx, "")

		return err
	}, pulumi.WithMocks("hetzner-iac", "test", stackMocks{outputs: resource.PropertyMap{}}))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cluster stack reference is empty")
}

func TestOutputNames_AreStable(t *testing.T) {
	t.Parallel()

	// These strings are the wire contract between the cluster tier and every
	// layer. Changing one without changing the other half produces a layer
	// that resolves an empty output and fails at apply, so pin them.
	assert.Equal(t, "kubeconfig", clusterref.OutputKubeconfig)
	assert.Equal(t, "talosconfig", clusterref.OutputTalosconfig)
	assert.Equal(t, "endpoint", clusterref.OutputEndpoint)
	assert.Equal(t, "apiLoadBalancerIp", clusterref.OutputAPILoadBalancerIP)
	assert.Equal(t, "networkId", clusterref.OutputNetworkID)
	assert.Equal(t, "podCidr", clusterref.OutputPodCIDR)
	assert.Equal(t, "serviceCidr", clusterref.OutputServiceCIDR)
	assert.Equal(t, "clusterName", clusterref.OutputClusterName)
	assert.Equal(t, "location", clusterref.OutputLocation)
}
