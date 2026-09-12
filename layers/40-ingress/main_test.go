package main

import (
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/chartsettings"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/layer/layertest"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/values"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/internals"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// testNodeSubnet is what the cluster tier publishes as nodeSubnet.
const testNodeSubnet = "10.0.1.0/24"

// rendered is the values YAML this layer would hand Helm, parsed.
//
// Through the template rather than around it: what reaches the cluster is the
// rendered file, so a test reading a Go map would be checking something the
// chart never sees.
func rendered(t *testing.T) map[string]any {
	t.Helper()

	data, err := internals.UnsafeAwaitOutput(t.Context(), IngressData(
		pulumi.String("platform-prod"),
		pulumi.String("hel1"),
		pulumi.String(testNodeSubnet),
		DefaultLoadBalancerType,
	))
	require.NoError(t, err)

	text, err := values.Render(Chart, data.Value)
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(text), &out), "the template must render valid yaml")

	return out
}

func nested(t *testing.T, in map[string]any, keys ...string) map[string]any {
	t.Helper()

	for _, key := range keys {
		next, ok := in[key].(map[string]any)
		require.True(t, ok, "no map at %q", key)

		in = next
	}

	return in
}

func annotations(t *testing.T) map[string]any {
	t.Helper()

	return nested(t, rendered(t), "service", "annotations")
}

func TestValues_ProxyProtocolIsSetOnBothSides(t *testing.T) {
	t.Parallel()

	// The annotation tells the load balancer to send the PROXY header; the
	// entry point has to be told which addresses may send one. Enabling one
	// side alone fails every request, which is why this is a test rather than
	// a comment.
	assert.Equal(t, "true", annotations(t)["load-balancer.hetzner.cloud/uses-proxyprotocol"])

	for _, entryPoint := range []string{
		chartsettings.TraefikEntryPointWeb,
		chartsettings.TraefikEntryPointTLS,
	} {
		trusted := nested(t, rendered(t),
			chartsettings.TraefikPorts, entryPoint, chartsettings.TraefikProxyProtocol)

		assert.Equal(t, []any{testNodeSubnet}, trusted[chartsettings.TraefikTrustedIPs],
			"entry point %q trusts nobody, so it rejects the header on every connection", entryPoint)
	}
}

func TestValues_DoesNotTrustForwardedHeaders(t *testing.T) {
	t.Parallel()

	// With PROXY protocol carrying the real client address, also trusting
	// X-Forwarded-For would accept a spoofed one. Traefik trusts nobody by
	// default, so the assertion is that nothing turns it on.
	for _, entryPoint := range []string{
		chartsettings.TraefikEntryPointWeb,
		chartsettings.TraefikEntryPointTLS,
	} {
		port := nested(t, rendered(t), chartsettings.TraefikPorts, entryPoint)

		assert.NotContains(t, port, "forwardedHeaders",
			"entry point %q trusts a forwarded header, which PROXY protocol makes spoofable", entryPoint)
	}
}

func TestValues_ReachesNodesOverThePrivateNetwork(t *testing.T) {
	t.Parallel()

	// Public targets would route traffic out of and back into Hetzner's
	// network, and would need firewall rules that otherwise do not exist.
	assert.Equal(t, "true", annotations(t)["load-balancer.hetzner.cloud/use-private-ip"])
}

func TestValues_KeepsTheClientAddress(t *testing.T) {
	t.Parallel()

	// The chart's service type is LoadBalancer by default, and the CCM only
	// creates a load balancer for that type — so what this asserts is the
	// part the layer sets.
	spec := nested(t, rendered(t), "service", "spec")

	assert.Equal(t, "Local", spec["externalTrafficPolicy"])
}

func TestValues_PlacementFollowsTheCluster(t *testing.T) {
	t.Parallel()

	// A load balancer in a different location than the nodes cannot use
	// private-network targets.
	got := annotations(t)

	assert.Equal(t, "hel1", got["load-balancer.hetzner.cloud/location"])
	assert.Equal(t, "platform-prod-ingress", got["load-balancer.hetzner.cloud/name"])
	assert.Equal(t, DefaultLoadBalancerType, got["load-balancer.hetzner.cloud/type"])
}

func TestValues_LeaveTheDashboardOff(t *testing.T) {
	t.Parallel()

	// Traefik's API serves the dashboard without authentication when
	// `api.insecure` is on. Nothing here turns it on, and this is what would
	// notice if something did.
	assert.NotContains(t, rendered(t), "api")
}

func TestValues_SurviveANodeFailure(t *testing.T) {
	t.Parallel()

	all := rendered(t)

	assert.Equal(t, float64(ControllerReplicas), nested(t, all, "deployment")["replicas"])
	assert.Equal(t, true, nested(t, all, "podDisruptionBudget")["enabled"])

	spread, ok := all["topologySpreadConstraints"].([]any)
	require.True(t, ok)
	require.Len(t, spread, 1)

	constraint, ok := spread[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "kubernetes.io/hostname", constraint["topologyKey"],
		"replicas must be spread across nodes, not just counted")

	// The selector has to be a label the chart stamps on the pods. A
	// constraint whose selector matches nothing is accepted by Kubernetes and
	// does nothing at all.
	labels := nested(t, constraint, "labelSelector", "matchLabels")
	assert.Equal(t, "traefik", labels["app.kubernetes.io/name"])
}

func TestComponents(t *testing.T) {
	t.Parallel()

	layertest.Check(t, Components)
}
