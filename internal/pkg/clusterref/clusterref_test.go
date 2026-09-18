package clusterref_test

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/internals"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterref"
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

// current is what a cluster tier applied today publishes: every declared
// output, the version among them, and the credentials marked secret the way
// Pulumi marks them on the wire.
func current() stackMocks {
	return stackMocks{outputs: resource.PropertyMap{
		resource.PropertyKey(clusterref.OutputContractVersion):   resource.NewNumberProperty(clusterref.ContractVersion),
		resource.PropertyKey(clusterref.OutputKubeconfig):        resource.MakeSecret(resource.NewStringProperty("apiVersion: v1")),
		resource.PropertyKey(clusterref.OutputTalosconfig):       resource.MakeSecret(resource.NewStringProperty("context: dev")),
		resource.PropertyKey(clusterref.OutputEndpoint):          resource.NewStringProperty("https://203.0.113.200:6443"),
		resource.PropertyKey(clusterref.OutputAPILoadBalancerIP): resource.NewStringProperty(""),
		resource.PropertyKey(clusterref.OutputNetworkID):         resource.NewNumberProperty(12637895),
		resource.PropertyKey(clusterref.OutputPodCIDR):           resource.NewStringProperty("10.244.0.0/16"),
		resource.PropertyKey(clusterref.OutputServiceCIDR):       resource.NewStringProperty("10.96.0.0/12"),
		resource.PropertyKey(clusterref.OutputClusterName):       resource.NewStringProperty("platform-prod"),
		resource.PropertyKey(clusterref.OutputLocation):          resource.NewStringProperty(clusterref.ProbeLocation),
		resource.PropertyKey(clusterref.OutputHcloudToken):       resource.MakeSecret(resource.NewStringProperty("token-from-the-cluster-tier")),
		resource.PropertyKey(clusterref.OutputControlPlaneCount): resource.NewNumberProperty(3),
	}}
}

// resolve runs Resolve against one producer shape and hands the result to fn.
func resolve(t *testing.T, mocks stackMocks, fn func(*clusterref.Cluster)) error {
	t.Helper()

	return pulumi.RunErr(func(ctx *pulumi.Context) error {
		cluster, err := clusterref.Resolve(ctx, "acme/hetzner-cluster/prod")
		if err != nil {
			return err
		}

		fn(cluster)

		return nil
	}, pulumi.WithMocks("hetzner-iac", "test", mocks))
}

// await resolves one output and returns the error it carries.
//
// Not ApplyT: an output nothing consumes is never awaited by the engine, so an
// error raised inside it hangs the program instead of failing it. In a real
// layer the kubeconfig reaches a provider and the token reaches a Secret,
// which is what makes the gate fire there; here it takes an explicit await.
func await(t *testing.T, mocks stackMocks, pick func(*clusterref.Cluster) pulumi.Output) (any, error) {
	t.Helper()

	var value any

	err := resolveErr(t, mocks, func(ctx *pulumi.Context, cluster *clusterref.Cluster) error {
		result, awaitErr := internals.UnsafeAwaitOutput(ctx.Context(), pick(cluster))
		if awaitErr != nil {
			return awaitErr
		}

		value = result.Value

		return nil
	})

	return value, err
}

func resolveErr(
	t *testing.T,
	mocks stackMocks,
	fn func(*pulumi.Context, *clusterref.Cluster) error,
) error {
	t.Helper()

	return pulumi.RunErr(func(ctx *pulumi.Context) error {
		cluster, err := clusterref.Resolve(ctx, "acme/hetzner-cluster/prod")
		if err != nil {
			return err
		}

		return fn(ctx, cluster)
	}, pulumi.WithMocks("hetzner-iac", "test", mocks))
}

func TestResolve_ReadsTheClusterTierOutputs(t *testing.T) {
	t.Parallel()

	var (
		endpoint    string
		podCIDR     string
		clusterName string
		token       string
		count       int
	)

	done := make(chan struct{})

	err := resolve(t, current(), func(cluster *clusterref.Cluster) {
		pulumi.All(cluster.Endpoint, cluster.PodCIDR, cluster.ClusterName,
			cluster.HcloudToken, cluster.ControlPlaneCount).
			ApplyT(func(values []any) error {
				endpoint, _ = values[0].(string)
				podCIDR, _ = values[1].(string)
				clusterName, _ = values[2].(string)
				token, _ = values[3].(string)
				count, _ = values[4].(int)

				close(done)

				return nil
			})
	})

	require.NoError(t, err)
	<-done

	assert.Equal(t, "https://203.0.113.200:6443", endpoint)
	assert.Equal(t, "10.244.0.0/16", podCIDR)
	assert.Equal(t, "platform-prod", clusterName)
	assert.Equal(t, "token-from-the-cluster-tier", token)
	assert.Equal(t, 3, count)
}

func TestResolve_KeepsSecretsSecret(t *testing.T) {
	t.Parallel()

	// The typed accessors carry secretness; a manual ApplyT(string) round-trip
	// would quietly drop it and hand each layer an unwrapped cluster-admin
	// kubeconfig to store in its own state. Nothing else reports that.
	err := resolve(t, current(), func(cluster *clusterref.Cluster) {
		for name, output := range map[string]pulumi.Output{
			"kubeconfig":  cluster.Kubeconfig,
			"hcloudToken": cluster.HcloudToken,
		} {
			assert.True(t, pulumi.IsSecret(output), "%s must stay a secret through the gate", name)
		}

		for name, output := range map[string]pulumi.Output{
			"clusterName":       cluster.ClusterName,
			"podCidr":           cluster.PodCIDR,
			"controlPlaneCount": cluster.ControlPlaneCount,
		} {
			assert.False(t, pulumi.IsSecret(output), "%s is not a credential and marking it one hides it from diffs", name)
		}
	})

	require.NoError(t, err)
}

func TestResolve_AProducerThatPredatesVersioningFailsWithOneCommand(t *testing.T) {
	t.Parallel()

	// The state every existing stack is in until it is applied again: outputs
	// present, no version among them. One error, naming the stack and the
	// command — not one error per output, worded differently each time.
	mocks := current()
	delete(mocks.outputs, resource.PropertyKey(clusterref.OutputContractVersion))

	_, err := await(t, mocks, func(c *clusterref.Cluster) pulumi.Output { return c.ContractCheck })

	require.Error(t, err)
	assert.Contains(t, err.Error(), "publishes contract v0")
	assert.Contains(t, err.Error(), "task cluster:apply")
	assert.Contains(t, err.Error(), "acme/hetzner-cluster/prod")
}

// TestResolve_TheCheckIsNotInAnyValue is the regression this file exists for
// now.
//
// The first version of the check routed every output through it, so the
// version landed in the kubernetes provider's INPUTS. The provider's identity
// then changed and Pulumi planned to replace it — and with it every Helm
// release and every Secret in every layer. On the live cluster that was the
// CNI, the cloud controller manager and the CSI driver being destroyed and
// recreated to improve an error message. Measured, not guessed: the plan went
// from `+-5 to replace` to `4 unchanged` when the wrapper came off.
func TestResolve_TheCheckIsNotInAnyValue(t *testing.T) {
	t.Parallel()

	// A stale producer: the check must fail, and every value must still
	// resolve to what the producer published. A value that failed here would
	// mean the check is wired into it again.
	mocks := current()
	mocks.outputs[resource.PropertyKey(clusterref.OutputContractVersion)] =
		resource.NewNumberProperty(clusterref.ContractVersion - 1)

	_, err := await(t, mocks, func(c *clusterref.Cluster) pulumi.Output { return c.ContractCheck })
	require.Error(t, err, "the check itself must report a stale producer")

	for name, read := range map[string]func(*clusterref.Cluster) pulumi.Output{
		"kubeconfig":        func(c *clusterref.Cluster) pulumi.Output { return c.Kubeconfig },
		"podCidr":           func(c *clusterref.Cluster) pulumi.Output { return c.PodCIDR },
		"controlPlaneCount": func(c *clusterref.Cluster) pulumi.Output { return c.ControlPlaneCount },
	} {
		_, valueErr := await(t, mocks, read)
		assert.NoError(t, valueErr,
			"%s must not depend on the check: that is what replaced the provider", name)
	}
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
	assert.Equal(t, "contractVersion", clusterref.OutputContractVersion)
	assert.Equal(t, "kubeconfig", clusterref.OutputKubeconfig)
	assert.Equal(t, "talosconfig", clusterref.OutputTalosconfig)
	assert.Equal(t, "endpoint", clusterref.OutputEndpoint)
	assert.Equal(t, "apiLoadBalancerIp", clusterref.OutputAPILoadBalancerIP)
	assert.Equal(t, "networkId", clusterref.OutputNetworkID)
	assert.Equal(t, "nodeSubnet", clusterref.OutputNodeSubnet)
	assert.Equal(t, "podCidr", clusterref.OutputPodCIDR)
	assert.Equal(t, "serviceCidr", clusterref.OutputServiceCIDR)
	assert.Equal(t, "clusterName", clusterref.OutputClusterName)
	assert.Equal(t, "location", clusterref.OutputLocation)
	assert.Equal(t, "hcloudToken", clusterref.OutputHcloudToken)
	assert.Equal(t, "controlPlaneCount", clusterref.OutputControlPlaneCount)
	assert.Equal(t, "routingMode", clusterref.OutputRoutingMode)
	assert.Equal(t, "domain", clusterref.OutputDomain)
	assert.Equal(t, "dnsZone", clusterref.OutputDNSZone)
}

// TestOutputNames_ArePinnedWithoutException catches the way the list above
// goes stale: three names were added to the contract and not to it, and the
// three were routingMode, domain and dnsZone — domain being the one two layers
// must spell identically.
//
// Counting the constants rather than listing them again, because a second list
// is a second thing to forget.
func TestOutputNames_ArePinnedWithoutException(t *testing.T) {
	t.Parallel()

	source, err := os.ReadFile("clusterref.go")
	require.NoError(t, err)

	declared := regexp.MustCompile(`(?m)^\tOutput\w+\s+= "`).FindAllString(string(source), -1)
	require.NotEmpty(t, declared, "no Output constants found; this test is checking nothing")

	pinned, err := os.ReadFile("clusterref_test.go")
	require.NoError(t, err)

	assertions := regexp.MustCompile(`clusterref\.Output\w+\)`).FindAllString(string(pinned), -1)

	unique := map[string]bool{}
	for _, found := range assertions {
		unique[found] = true
	}

	// Equal on the counts rather than assert.Len on the map: Len prints the
	// whole collection, and a failure here is about a number.
	assert.Equal(t, len(declared), len(unique),
		"the contract declares %d output names and TestOutputNames_AreStable pins %d of them",
		len(declared), len(unique))
}

// The producer side of this contract is checked where the producer lives:
// infra/cluster's TestExports_CoverEveryDeclaredOutput compares the names it
// publishes with Declared, and TestExports_CarryTheValueEachNamePromises
// compares the values.
//
// It used to be here, reading main.go as text and looking for each
// ctx.Export. That could only see names — podCidr wired to nodeSubnet passed
// it — and a package main cannot be imported, which is why it was text at
// all. The producer now builds its exports as a map, so the check is an
// assertion in its own package.

func TestDeclared_ListsEveryOutputConstant(t *testing.T) {
	t.Parallel()

	// Declared is what the producer test iterates, so a constant missing from
	// it is a constant nothing checks.
	raw, err := os.ReadFile("clusterref.go")
	require.NoError(t, err)

	var constants int

	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Output") && strings.Contains(line, "= \"") {
			constants++
		}
	}

	assert.Equal(t, constants, len(clusterref.Declared),
		"%d Output constants but %d in Declared", constants, len(clusterref.Declared))
}
