package main

import (
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/charts"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/layer/layertest"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/values"

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

	data, err := internals.UnsafeAwaitOutput(t.Context(),
		IngressData(pulumi.String(testNodeSubnet)))
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

func TestValues_ProxyProtocolIsTrustedOnBothEntryPoints(t *testing.T) {
	t.Parallel()

	// One half of the pair. The load balancer is told to SEND the header in
	// internal/pkg/hetzner; here each entry point is told which addresses may send one.
	// Either half alone fails every request, which is why both are tests.
	for _, entryPoint := range []string{
		charts.TraefikEntryPointWeb,
		charts.TraefikEntryPointTLS,
	} {
		trusted := nested(t, rendered(t),
			charts.TraefikPorts, entryPoint, charts.TraefikProxyProtocol)

		assert.Equal(t, []any{testNodeSubnet}, trusted[charts.TraefikTrustedIPs],
			"entry point %q trusts nobody, so it rejects the header on every connection", entryPoint)
	}
}

func TestValues_DoesNotTrustForwardedHeaders(t *testing.T) {
	t.Parallel()

	// With PROXY protocol carrying the real client address, also trusting
	// X-Forwarded-For would accept a spoofed one. Traefik trusts nobody by
	// default, so the assertion is that nothing turns it on.
	for _, entryPoint := range []string{
		charts.TraefikEntryPointWeb,
		charts.TraefikEntryPointTLS,
	} {
		port := nested(t, rendered(t), charts.TraefikPorts, entryPoint)

		assert.NotContains(t, port, "forwardedHeaders",
			"entry point %q trusts a forwarded header, which PROXY protocol makes spoofable", entryPoint)
	}
}

func TestValues_AskForANodePortRatherThanALoadBalancer(t *testing.T) {
	t.Parallel()

	// The chart's default is a Service of type LoadBalancer, which hands the
	// job to the cloud controller manager. Pulumi owns the load balancer now,
	// and leaving this at the default would have both reconciling one object.
	spec := nested(t, rendered(t),
		charts.TraefikService, charts.TraefikServiceSpec)

	assert.Equal(t, "NodePort", spec[charts.TraefikServiceType])

	// Local, so the client address survives without a second hop. It is also
	// what makes the load balancer's health check meaningful: a node with no
	// Traefik pod does not answer on the node port and drops out of rotation.
	assert.Equal(t, "Local", spec["externalTrafficPolicy"])
}

func TestValues_PinTheNodePortsTheLoadBalancerForwardsTo(t *testing.T) {
	t.Parallel()

	// The contract with internal/pkg/hetzner, and the reason both sides read
	// internal/pkg/platform. Unpinned, Kubernetes allocates from 30000-32767 and the
	// load balancer health-checks a port nothing listens on — every target
	// unhealthy, with nothing else in the cluster looking wrong.
	for entryPoint, want := range map[string]int{
		charts.TraefikEntryPointWeb: platform.IngressNodePortHTTP,
		charts.TraefikEntryPointTLS: platform.IngressNodePortHTTPS,
	} {
		port := nested(t, rendered(t), charts.TraefikPorts, entryPoint)

		assert.Equal(t, float64(want), port[charts.TraefikNodePort],
			"entry point %q does not pin its node port", entryPoint)
	}
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

	// nil provider: layertest never calls Create, so the set can be built
	// without one — it checks ordering, chart pins and declared workloads.
	layertest.Check(t, components(nil))
}
