package hetzner_test

import (
	"strings"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterref"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/hetzner"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testCluster is the cluster name the ingress tests build against.
const testCluster = "platform-prod"

// testNetworkID is what the cluster tier publishes as networkId.
const testNetworkID = 12648820

// runIngress creates an ingress load balancer under the mock monitor and
// returns what was registered.
func runIngress(t *testing.T) *recorder {
	t.Helper()

	rec := newRecorder()

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := hetzner.NewIngressLoadBalancer(ctx, "ingress", hetzner.IngressLoadBalancerArgs{
			ClusterName:      pulumi.String(testCluster),
			Location:         pulumi.String(clusterref.ProbeLocation),
			NetworkID:        pulumi.Int(testNetworkID),
			LoadBalancerType: "lb11",
		})

		return err
	}, pulumi.WithMocks("hetzner-iac", "test", rec))

	require.NoError(t, err)

	return rec
}

func TestIngressLoadBalancer_IsPlacedAndLabelledWithTheCluster(t *testing.T) {
	t.Parallel()

	registered := runIngress(t).of("hcloud:index/loadBalancer:LoadBalancer")
	require.Len(t, registered, 1)

	got := registered[0]

	// The name carries the cluster's, so two clusters in one Hetzner project
	// do not collide on it.
	assert.Equal(t, testCluster+"-ingress", got["name"].StringValue())
	assert.Equal(t, "lb11", got["loadBalancerType"].StringValue())

	// Location has to match the servers'. Elsewhere in the same network zone
	// still works and pays for a detour on every request.
	assert.Equal(t, clusterref.ProbeLocation, got["location"].StringValue())

	// The cluster label is not decoration: it is what the target selector
	// below matches, so a load balancer without it targets nothing.
	labels := got["labels"].ObjectValue()
	assert.Equal(t, testCluster, labels[resource.PropertyKey(hetzner.LabelCluster)].StringValue())
}

func TestIngressLoadBalancer_ServesBothEntryPointsOnThePinnedNodePorts(t *testing.T) {
	t.Parallel()

	registered := runIngress(t).of("hcloud:index/loadBalancerService:LoadBalancerService")
	require.Len(t, registered, 2, "an ingress needs both 80 and 443")

	byListenPort := map[float64]resource.PropertyMap{}
	for _, service := range registered {
		byListenPort[service["listenPort"].NumberValue()] = service
	}

	for listen, wantNodePort := range map[float64]int{
		80:  platform.IngressNodePortHTTP,
		443: platform.IngressNodePortHTTPS,
	} {
		service, found := byListenPort[listen]
		require.True(t, found, "nothing listens on %v", listen)

		// The contract with internal/pkg/values/traefik.yaml.tmpl. Both read
		// internal/pkg/platform, so this asserts the pin reached the load balancer.
		assert.Equal(t, float64(wantNodePort), service["destinationPort"].NumberValue(),
			"port %v forwards somewhere other than the pinned node port", listen)

		// PROXY protocol, the half the load balancer owns. Without it Traefik
		// waits for a header nobody sends; without the trust list on the other
		// side, Traefik rejects the one this sends.
		assert.True(t, service["proxyprotocol"].BoolValue(),
			"port %v does not send the PROXY header, so the client address is lost", listen)

		// The health check has to probe the NODE PORT, not the listen port.
		// Probing 80 would ask the node whether the load balancer is up.
		check := service["healthCheck"].ObjectValue()
		assert.Equal(t, float64(wantNodePort), check["port"].NumberValue(),
			"port %v health-checks something other than the node port it forwards to", listen)
		assert.Equal(t, "tcp", check["protocol"].StringValue())
	}
}

func TestIngressLoadBalancer_TargetsEveryNodeInTheClusterPrivately(t *testing.T) {
	t.Parallel()

	registered := runIngress(t).of("hcloud:index/loadBalancerTarget:LoadBalancerTarget")
	require.Len(t, registered, 1)

	got := registered[0]

	// A label selector rather than a list of servers, so a node created later
	// joins the target set with no diff here — and so this works at all on a
	// control-plane-only cluster. The cloud controller manager refuses to
	// target a node carrying
	// node.kubernetes.io/exclude-from-external-load-balancers, Talos puts that
	// on every control-plane node, and the measured result was a load balancer
	// with an address and zero targets.
	assert.Equal(t, "label_selector", got["type"].StringValue())
	assert.Equal(t, hetzner.ClusterSelector(testCluster), got["labelSelector"].StringValue())

	// Private addresses. Public ones would send traffic out of and back into
	// Hetzner's network — metered, slower, and needing firewall rules that
	// otherwise do not exist.
	assert.True(t, got["usePrivateIp"].BoolValue())
}

func TestIngressLoadBalancer_TargetWaitsForTheNetworkAttachment(t *testing.T) {
	t.Parallel()

	// Ordering, not an input, and this one is the difference between an apply
	// that works and one that fails after creating everything else. Hetzner
	// refuses a private-address target on a load balancer that is not attached
	// to a network yet, and the API load balancer hit exactly that:
	//
	//	add label selector target: load balancer is not attached to a network
	//	(load_balancer_not_attached_to_network)
	//
	// Nothing about the code looked wrong; Pulumi was simply free to create
	// the two in parallel.
	depends := runIngress(t).dependsOn["hcloud:index/loadBalancerTarget:LoadBalancerTarget"]
	require.NotEmpty(t, depends, "the ingress target was registered with no dependencies at all")

	var onAttachment bool

	for _, urn := range depends {
		if strings.Contains(urn, "loadBalancerNetwork:LoadBalancerNetwork") {
			onAttachment = true
		}
	}

	assert.True(t, onAttachment,
		"the ingress target does not depend on the network attachment, so Pulumi may create "+
			"them in parallel and the apply fails with load_balancer_not_attached_to_network")
}

func TestIngressLoadBalancer_AttachesToTheClustersNetwork(t *testing.T) {
	t.Parallel()

	registered := runIngress(t).of("hcloud:index/loadBalancerNetwork:LoadBalancerNetwork")
	require.Len(t, registered, 1)

	assert.Equal(t, float64(testNetworkID), registered[0]["networkId"].NumberValue(),
		"the load balancer is not in the cluster's network, so no private target can work")
}
