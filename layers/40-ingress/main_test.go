package main

import (
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/chartsettings"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func values(t *testing.T) (controller, service, annotations, controllerConfig pulumi.Map) {
	t.Helper()

	all := IngressValues(pulumi.String("platform-prod-ingress"), pulumi.String("hel1"), DefaultLoadBalancerType)

	controller, ok := all["controller"].(pulumi.Map)
	require.True(t, ok)

	service, ok = controller["service"].(pulumi.Map)
	require.True(t, ok)

	annotations, ok = service["annotations"].(pulumi.Map)
	require.True(t, ok)

	controllerConfig, ok = controller["config"].(pulumi.Map)
	require.True(t, ok)

	return controller, service, annotations, controllerConfig
}

func TestIngressValues_ProxyProtocolIsSetOnBothSides(t *testing.T) {
	t.Parallel()

	// The annotation tells the load balancer to send the PROXY header; the
	// controller config tells nginx to expect it. Enabling one alone makes
	// every request fail to parse — which is why this is worth a test rather
	// than a comment.
	_, _, annotations, controllerConfig := values(t)

	assert.Equal(t, pulumi.String("true"), annotations["load-balancer.hetzner.cloud/uses-proxyprotocol"])
	assert.Equal(t, pulumi.String("true"), controllerConfig[chartsettings.IngressUseProxyProtocol])
}

func TestIngressValues_DoesNotTrustForwardedHeaders(t *testing.T) {
	t.Parallel()

	// With PROXY protocol carrying the real client address, also trusting
	// X-Forwarded-For would accept a spoofed one.
	_, _, _, controllerConfig := values(t)

	assert.Equal(t, pulumi.String("false"), controllerConfig[chartsettings.IngressUseForwardedHeaders])
}

func TestIngressValues_ReachesNodesOverThePrivateNetwork(t *testing.T) {
	t.Parallel()

	// Public targets would route traffic out of and back into Hetzner's
	// network, and would need firewall rules that otherwise do not exist.
	_, _, annotations, _ := values(t)

	assert.Equal(t, pulumi.String("true"), annotations["load-balancer.hetzner.cloud/use-private-ip"])
}

func TestIngressValues_AsksForALoadBalancerService(t *testing.T) {
	t.Parallel()

	// The Service type is the whole integration: the CCM only creates a load
	// balancer for a Service of type LoadBalancer.
	_, service, _, _ := values(t)

	assert.Equal(t, pulumi.String("LoadBalancer"), service["type"])
	assert.Equal(t, pulumi.String("Local"), service["externalTrafficPolicy"])
}

func TestIngressValues_PlacementFollowsTheCluster(t *testing.T) {
	t.Parallel()

	// A load balancer in a different location than the nodes cannot use
	// private-network targets.
	_, _, annotations, _ := values(t)

	assert.Equal(t, pulumi.String("hel1"), annotations["load-balancer.hetzner.cloud/location"])
	assert.Equal(t, pulumi.String("platform-prod-ingress"), annotations["load-balancer.hetzner.cloud/name"])
	assert.Equal(t, pulumi.String(DefaultLoadBalancerType), annotations["load-balancer.hetzner.cloud/type"])
}

func TestIngressValues_RefusesSnippetAnnotations(t *testing.T) {
	t.Parallel()

	// Snippet annotations let any namespace inject nginx configuration, which
	// is an escalation path out of a tenant namespace.
	controller, _, _, _ := values(t)

	assert.Equal(t, pulumi.Bool(false), controller["allowSnippetAnnotations"])
}

func TestIngressValues_SurvivesANodeFailure(t *testing.T) {
	t.Parallel()

	controller, _, _, _ := values(t)

	assert.Equal(t, pulumi.Int(2), controller["replicaCount"])

	budget, ok := controller["podDisruptionBudget"].(pulumi.Map)
	require.True(t, ok)
	assert.Equal(t, pulumi.Bool(true), budget["enabled"])

	spread, ok := controller["topologySpreadConstraints"].(pulumi.Array)
	require.True(t, ok)
	require.Len(t, spread, 1)

	constraint, ok := spread[0].(pulumi.Map)
	require.True(t, ok)
	assert.Equal(t, pulumi.String("kubernetes.io/hostname"), constraint["topologyKey"],
		"replicas must be spread across nodes, not just counted")
}
