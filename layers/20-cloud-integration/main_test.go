package main

import (
	"encoding/json"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/clusterref"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/pulumilog"

	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCCMValues_EnablesTheRouteController(t *testing.T) {
	t.Parallel()

	// Cilium is configured for native routing, which depends on the CCM
	// writing a route per node. With the route controller off, the CCM starts
	// cleanly and manages no routes — and pods cannot reach pods on other
	// nodes, with nothing in either component's logs saying why.
	podCIDR := pulumi.String("10.244.0.0/16")

	networking, ok := CCMValues(podCIDR)["networking"].(pulumi.Map)
	require.True(t, ok)

	assert.Equal(t, pulumi.Bool(true), networking["enabled"])
	assert.Equal(t, podCIDR, networking["clusterCIDR"])
}

func TestCCMValues_ReadsBothCredentialKeys(t *testing.T) {
	t.Parallel()

	// The network id is as necessary as the token: without it the route
	// controller has no network to write routes into.
	env, ok := CCMValues(pulumi.String("10.244.0.0/16"))["env"].(pulumi.Map)
	require.True(t, ok)

	for name, key := range map[string]string{
		"HCLOUD_TOKEN":   "token",
		"HCLOUD_NETWORK": "network",
	} {
		entry, ok := env[name].(pulumi.Map)
		require.True(t, ok, name)

		valueFrom, ok := entry["valueFrom"].(pulumi.Map)
		require.True(t, ok, name)

		ref, ok := valueFrom["secretKeyRef"].(pulumi.Map)
		require.True(t, ok, name)

		assert.Equal(t, pulumi.String(CredentialsSecret), ref["name"], name)
		assert.Equal(t, pulumi.String(key), ref["key"], name)
	}
}

func TestSecretRef_PointsAtTheSharedSecret(t *testing.T) {
	t.Parallel()

	// Both charts default to a secret with this name; a mismatch produces
	// pods that start and then fail to authenticate against the Hetzner API.
	ref := SecretRef("token")

	valueFrom, ok := ref["valueFrom"].(pulumi.Map)
	require.True(t, ok)

	secretKeyRef, ok := valueFrom["secretKeyRef"].(pulumi.Map)
	require.True(t, ok)

	assert.Equal(t, pulumi.String("hcloud"), secretKeyRef["name"])
	assert.Equal(t, pulumi.String("token"), secretKeyRef["key"])
}

// ---------------------------------------------------------------------------
// Where the token comes from
// ---------------------------------------------------------------------------

const (
	testProject = "cloud-integration"
	testStack   = "test"
)

// stackMocks stands in for the cluster tier. exported == "" models a cluster
// built from an environment token rather than from stack config, which the
// tier exports as an empty string.
type stackMocks struct{ exported string }

func (m stackMocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	if args.TypeToken != "pulumi:pulumi:StackReference" {
		return args.Name, args.Inputs, nil
	}

	// Every declared output, always: the contract is total, and the tier
	// exports an empty token rather than none when it has none.
	outputs := resource.PropertyMap{
		resource.PropertyKey(clusterref.OutputContractVersion):   resource.NewNumberProperty(clusterref.ContractVersion),
		resource.PropertyKey(clusterref.OutputKubeconfig):        resource.NewStringProperty("apiVersion: v1"),
		resource.PropertyKey(clusterref.OutputTalosconfig):       resource.NewStringProperty("context: test"),
		resource.PropertyKey(clusterref.OutputEndpoint):          resource.NewStringProperty("https://203.0.113.200:6443"),
		resource.PropertyKey(clusterref.OutputAPILoadBalancerIP): resource.NewStringProperty(""),
		resource.PropertyKey(clusterref.OutputNetworkID):         resource.NewNumberProperty(12637895),
		resource.PropertyKey(clusterref.OutputPodCIDR):           resource.NewStringProperty("10.244.0.0/16"),
		resource.PropertyKey(clusterref.OutputServiceCIDR):       resource.NewStringProperty("10.96.0.0/12"),
		resource.PropertyKey(clusterref.OutputClusterName):       resource.NewStringProperty("platform-test"),
		resource.PropertyKey(clusterref.OutputLocation):          resource.NewStringProperty("hel1"),
		resource.PropertyKey(clusterref.OutputHcloudToken):       resource.NewStringProperty(m.exported),
		resource.PropertyKey(clusterref.OutputControlPlaneCount): resource.NewNumberProperty(3),
	}

	return args.Name, resource.PropertyMap{"outputs": resource.NewObjectProperty(outputs)}, nil
}

func (stackMocks) Call(pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

// resolved runs resolveToken against a cluster stack exporting `exported` and
// a layer config holding `override`, and returns the token the layer would put
// into the credentials Secret.
//
// Config reaches a Pulumi program through PULUMI_CONFIG, and t.Setenv forbids
// t.Parallel, so these tests run in sequence.
func resolved(t *testing.T, exported, override string) (string, error) {
	t.Helper()

	cfg := map[string]string{}
	if override != "" {
		cfg[testProject+":hcloudToken"] = override
	}

	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	t.Setenv("PULUMI_CONFIG", string(raw))

	var (
		got  string
		done = make(chan struct{})
	)

	err = pulumi.RunErr(func(ctx *pulumi.Context) error {
		cluster, resolveErr := clusterref.Resolve(ctx, "acme/hetzner-cluster/test")
		if resolveErr != nil {
			return resolveErr
		}

		token := resolveToken(config.New(ctx, testProject), cluster, pulumilog.New(ctx))

		// The token has to reach a resource. An output nothing consumes is
		// never awaited, so an error raised inside it hangs the program
		// instead of failing it — which is how this test first behaved.
		// main() puts the token in exactly this Secret.
		if _, secretErr := corev1.NewSecret(ctx, CredentialsSecret, &corev1.SecretArgs{
			StringData: pulumi.StringMap{"token": token},
		}); secretErr != nil {
			return secretErr
		}

		token.ApplyT(func(value string) string {
			got = value

			close(done)

			return value
		})

		return nil
	}, pulumi.WithMocks(testProject, testStack, stackMocks{exported: exported}))
	if err != nil {
		// The apply never ran, so `done` stays open — returning here rather
		// than waiting is what keeps a failing case from hanging the suite.
		return "", err
	}

	<-done

	return got, nil
}

func TestResolveToken_TakesTheClusterTiersToken(t *testing.T) {
	// The point of the whole arrangement: the token is set once, in the stack
	// whose provider already holds it, and this layer keeps no copy.
	got, err := resolved(t, "token-from-the-cluster-tier", "")

	require.NoError(t, err)
	assert.Equal(t, "token-from-the-cluster-tier", got)
}

func TestResolveToken_LayerConfigOverridesTheClusterTier(t *testing.T) {
	// Kept deliberately: an operator may want the CCM and CSI to authenticate
	// with a token scoped differently from the one that built the cluster.
	got, err := resolved(t, "token-from-the-cluster-tier", "token-from-this-layer")

	require.NoError(t, err)
	assert.Equal(t, "token-from-this-layer", got)
}

func TestResolveToken_NoTokenAnywhereFailsWithTheRemedy(t *testing.T) {
	// Without this the layer creates a Secret holding an empty token, and the
	// CCM starts, logs 401 and never clears the uninitialized taint — which
	// reads as a broken cluster rather than as a missing credential.
	_, err := resolved(t, "", "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), clusterref.OutputHcloudToken)
	assert.Contains(t, err.Error(), "config set --secret hcloud:token")
	assert.Contains(t, err.Error(), "config set --secret cloud-integration:hcloudToken")
}
