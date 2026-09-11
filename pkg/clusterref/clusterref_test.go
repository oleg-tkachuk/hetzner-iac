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
		resource.PropertyKey(clusterref.OutputHcloudToken): resource.NewStringProperty("token-from-the-cluster-tier"),
	}}

	var (
		endpoint    string
		podCIDR     string
		clusterName string
		hcloudToken string
	)

	done := make(chan struct{})

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		cluster, err := clusterref.Resolve(ctx, "acme/hetzner-cluster/prod")
		if err != nil {
			return err
		}

		pulumi.All(cluster.Endpoint, cluster.PodCIDR, cluster.ClusterName, cluster.HcloudToken).
			ApplyT(func(values []any) error {
				endpoint, _ = values[0].(string)
				podCIDR, _ = values[1].(string)
				clusterName, _ = values[2].(string)
				hcloudToken, _ = values[3].(string)

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
	// The token reaches a layer through the same reference as the kubeconfig,
	// which is what lets 20-cloud-integration hold no copy of its own. The
	// mock returns it unmarked; the producer's config.GetSecret is what makes
	// it a secret, and that is not observable from here.
	assert.Equal(t, "token-from-the-cluster-tier", hcloudToken)
}

func TestResolve_AMissingTokenIsEmptyRatherThanAnError(t *testing.T) {
	t.Parallel()

	// A cluster stack applied before the token was exported simply has no such
	// output. The SDK's GetStringOutput would fail that with "does not exist
	// on stack"; Resolve deliberately returns empty instead, so the consumer
	// that needs the token is the one that says what to do about it.
	mocks := stackMocks{outputs: resource.PropertyMap{
		resource.PropertyKey(clusterref.OutputKubeconfig): resource.NewStringProperty("apiVersion: v1"),
	}}

	var (
		token string
		done  = make(chan struct{})
	)

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		cluster, err := clusterref.Resolve(ctx, "acme/hetzner-cluster/prod")
		if err != nil {
			return err
		}

		cluster.HcloudToken.ApplyT(func(value string) string {
			token = value

			close(done)

			return value
		})

		return nil
	}, pulumi.WithMocks("hetzner-iac", "test", mocks))

	require.NoError(t, err)
	<-done

	assert.Empty(t, token)
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
	assert.Equal(t, "hcloudToken", clusterref.OutputHcloudToken)
}
