package main

import (
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/layer/layertest"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/chartsettings"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testNodeSubnet is what the cluster tier publishes as nodeSubnet.
const testNodeSubnet = "10.0.1.0/24"

func values(t *testing.T) pulumi.Map {
	t.Helper()

	return IngressValues(
		pulumi.String("platform-prod-ingress"),
		pulumi.String("hel1"),
		pulumi.String(testNodeSubnet),
		DefaultLoadBalancerType,
	)
}

func nested(t *testing.T, in pulumi.Map, keys ...string) pulumi.Map {
	t.Helper()

	for _, key := range keys {
		next, ok := in[key].(pulumi.Map)
		require.True(t, ok, "no map at %q", key)

		in = next
	}

	return in
}

func annotations(t *testing.T) pulumi.Map {
	t.Helper()

	return nested(t, values(t), "service", "annotations")
}

func TestIngressValues_ProxyProtocolIsSetOnBothSides(t *testing.T) {
	t.Parallel()

	// The annotation tells the load balancer to send the PROXY header; the
	// entry point has to be told which addresses may send one. Enabling one
	// side alone fails every request, which is why this is a test rather than
	// a comment.
	assert.Equal(t, pulumi.String("true"),
		annotations(t)["load-balancer.hetzner.cloud/uses-proxyprotocol"])

	for _, entryPoint := range []string{
		chartsettings.TraefikEntryPointWeb,
		chartsettings.TraefikEntryPointTLS,
	} {
		trusted := nested(t, values(t),
			chartsettings.TraefikPorts, entryPoint, chartsettings.TraefikProxyProtocol)

		assert.Equal(t, pulumi.StringArray{pulumi.String(testNodeSubnet)},
			trusted[chartsettings.TraefikTrustedIPs],
			"entry point %q trusts nobody, so it rejects the header on every connection", entryPoint)
	}
}

func TestIngressValues_TrustsOnlyTheNodeSubnet(t *testing.T) {
	t.Parallel()

	// The trust list decides who may claim to be someone else. It comes from
	// the cluster tier, so it is the range the nodes are actually in — a
	// wider one would accept a spoofed PROXY header from any pod.
	trusted := nested(t, values(t),
		chartsettings.TraefikPorts, chartsettings.TraefikEntryPointWeb,
		chartsettings.TraefikProxyProtocol)

	list, ok := trusted[chartsettings.TraefikTrustedIPs].(pulumi.StringArray)
	require.True(t, ok)
	require.Len(t, list, 1, "one range, the one the tier published")
}

func TestIngressValues_DoesNotTrustForwardedHeaders(t *testing.T) {
	t.Parallel()

	// With PROXY protocol carrying the real client address, also trusting
	// X-Forwarded-For would accept a spoofed one. Traefik's default is to
	// trust nobody, so the assertion is that nothing here turns it on —
	// setting the default explicitly would render identically and this test
	// would not notice a later change that did.
	for _, entryPoint := range []string{
		chartsettings.TraefikEntryPointWeb,
		chartsettings.TraefikEntryPointTLS,
	} {
		port := nested(t, values(t), chartsettings.TraefikPorts, entryPoint)

		assert.NotContains(t, port, "forwardedHeaders",
			"entry point %q trusts a forwarded header, which PROXY protocol makes spoofable", entryPoint)
	}
}

func TestIngressValues_ReachesNodesOverThePrivateNetwork(t *testing.T) {
	t.Parallel()

	// Public targets would route traffic out of and back into Hetzner's
	// network, and would need firewall rules that otherwise do not exist.
	assert.Equal(t, pulumi.String("true"),
		annotations(t)["load-balancer.hetzner.cloud/use-private-ip"])
}

func TestIngressValues_AsksForALoadBalancerService(t *testing.T) {
	t.Parallel()

	// The chart's service type is LoadBalancer by default, and the CCM only
	// creates a load balancer for that type — so what this asserts is the
	// part the layer sets: the traffic policy that keeps the client address.
	spec := nested(t, values(t), "service", "spec")

	assert.Equal(t, pulumi.String("Local"), spec["externalTrafficPolicy"])
}

func TestIngressValues_PlacementFollowsTheCluster(t *testing.T) {
	t.Parallel()

	// A load balancer in a different location than the nodes cannot use
	// private-network targets.
	got := annotations(t)

	assert.Equal(t, pulumi.String("hel1"), got["load-balancer.hetzner.cloud/location"])
	assert.Equal(t, pulumi.String("platform-prod-ingress"), got["load-balancer.hetzner.cloud/name"])
	assert.Equal(t, pulumi.String(DefaultLoadBalancerType), got["load-balancer.hetzner.cloud/type"])
}

func TestIngressValues_LeavesTheDashboardOff(t *testing.T) {
	t.Parallel()

	// Traefik's API serves the dashboard without authentication when
	// `api.insecure` is on. Nothing here turns it on, and this test is what
	// would notice if something did.
	assert.NotContains(t, values(t), "api")
}

func TestIngressValues_SurvivesANodeFailure(t *testing.T) {
	t.Parallel()

	all := values(t)

	assert.Equal(t, pulumi.Int(ControllerReplicas),
		nested(t, all, "deployment")["replicas"])

	budget := nested(t, all, "podDisruptionBudget")
	assert.Equal(t, pulumi.Bool(true), budget["enabled"])

	spread, ok := all["topologySpreadConstraints"].(pulumi.Array)
	require.True(t, ok)
	require.Len(t, spread, 1)

	constraint, ok := spread[0].(pulumi.Map)
	require.True(t, ok)
	assert.Equal(t, pulumi.String("kubernetes.io/hostname"), constraint["topologyKey"],
		"replicas must be spread across nodes, not just counted")

	// The selector has to be a label the chart stamps on the pods. A
	// constraint whose selector matches nothing is accepted by Kubernetes and
	// does nothing at all.
	labels := nested(t, constraint, "labelSelector", "matchLabels")
	assert.Equal(t, pulumi.String("traefik"), labels["app.kubernetes.io/name"])
}

func TestComponents(t *testing.T) {
	t.Parallel()

	layertest.Check(t, Components)
}
