package hetzner

import (
	"fmt"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/pulumiopts"

	"github.com/pulumi/pulumi-hcloud/sdk/go/hcloud"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// Ports the ingress load balancer listens on. HTTP and HTTPS, and nothing
// else: an ingress that answers on another port is a decision, not a default.
const (
	PortHTTP  = 80
	PortHTTPS = 443
)

// Health check timings for the ingress services.
//
// Deliberately the same as the API load balancer's: three failures at ten
// seconds takes a node out in about half a minute, which is slower than a pod
// restart and faster than a person notices.
const (
	healthCheckInterval = 10
	healthCheckTimeout  = 5
	healthCheckRetries  = 3
)

// IngressLoadBalancerArgs is what an ingress load balancer needs that the
// cluster tier already publishes.
type IngressLoadBalancerArgs struct {
	// ClusterName scopes the name, the labels and the target selector. An
	// Input because it arrives from a stack reference, not from a topology
	// file: this runs in the ingress layer, which reads the cluster tier.
	ClusterName pulumi.StringInput
	// Location has to match the servers'. A load balancer elsewhere in the
	// same network zone still works and pays for a detour on every request.
	Location pulumi.StringInput
	// NetworkID is the private network the load balancer attaches to, so it
	// can reach nodes by their private addresses.
	NetworkID pulumi.IntInput
	// LoadBalancerType is the Hetzner type, from stack config.
	LoadBalancerType string
}

// NewIngressLoadBalancer creates the load balancer that fronts the ingress
// controller, and returns its IPv4 address.
//
// Pulumi owns it rather than the cloud controller manager, and that is a
// deliberate reversal. The CCM turns a Service of type LoadBalancer into a
// real one, which is less code here — and it cost this repository two things,
// both measured:
//
//   - The load balancer was invisible to this repository. Nothing in `plan` or
//     `destroy` mentioned it; it appeared only in Hetzner's bill, and
//     `cluster:orphans` exists because of that gap.
//   - It refused to work at all. The CCM will not make a node carrying
//     node.kubernetes.io/exclude-from-external-load-balancers a target, Talos
//     puts that label on every control-plane node, and this cluster is three
//     of those. The load balancer came up on an address with zero targets:
//     "There are no available nodes for LoadBalancer".
//
// A label_selector target is a Hetzner concept and knows nothing about that
// Kubernetes label, so this works on a control-plane-only cluster today and
// keeps working when a worker pool arrives — new servers carry the cluster
// label and join the target set with no diff here.
//
// What is given up: the CCM reacts to a change in the Service, and this does
// not. Changing a port is an apply. That is the trade this repository makes
// everywhere else too.
func NewIngressLoadBalancer(
	ctx *pulumi.Context,
	name string,
	args IngressLoadBalancerArgs,
	opts ...pulumi.ResourceOption,
) (*IngressLoadBalancer, error) {
	labels := pulumi.StringMap{
		LabelCluster:   args.ClusterName,
		LabelManagedBy: pulumi.String(ManagedBy),
	}

	loadBalancer, err := hcloud.NewLoadBalancer(ctx, name, &hcloud.LoadBalancerArgs{
		Name:             pulumi.Sprintf("%s-ingress", args.ClusterName),
		LoadBalancerType: pulumi.String(args.LoadBalancerType),
		Location:         args.Location,
		Labels:           labels,
	}, opts...)
	if err != nil {
		return nil, fmt.Errorf("hcloud ingress load balancer: %w", err)
	}

	// Kept: everything below reaches the nodes privately, and Hetzner refuses
	// a private-address target on a load balancer that is not in a network
	// yet. The API load balancer learned this the expensive way — with only
	// the subnet in DependsOn, Pulumi created the attachment and the target in
	// parallel and the apply failed with
	// `load_balancer_not_attached_to_network`.
	attachment, err := hcloud.NewLoadBalancerNetwork(ctx, name+"-network",
		&hcloud.LoadBalancerNetworkArgs{
			LoadBalancerId: ingressID(loadBalancer),
			NetworkId:      args.NetworkID,
		}, opts...)
	if err != nil {
		return nil, fmt.Errorf("hcloud ingress load balancer network: %w", err)
	}

	// Both entry points, and neither is optional: an ingress serving only 80
	// cannot complete an ACME HTTP-01 redirect to itself, and one serving only
	// 443 has nothing to redirect from.
	for _, service := range []struct {
		suffix     string
		listenPort int
		nodePort   int
	}{
		{suffix: "-http", listenPort: PortHTTP, nodePort: platform.IngressNodePortHTTP},
		{suffix: "-https", listenPort: PortHTTPS, nodePort: platform.IngressNodePortHTTPS},
	} {
		if _, err := hcloud.NewLoadBalancerService(ctx, name+service.suffix,
			&hcloud.LoadBalancerServiceArgs{
				LoadBalancerId:  loadBalancer.ID().ToStringOutput(),
				Protocol:        pulumi.String("tcp"),
				ListenPort:      pulumi.Int(service.listenPort),
				DestinationPort: pulumi.Int(service.nodePort),
				// One half of the PROXY protocol pair. The other is the trust
				// list in internal/pkg/values/traefik.yaml.tmpl: Traefik trusts the
				// header from nobody by default, so this without that rejects
				// every connection, and that without this makes Traefik wait
				// for a header nobody sends.
				Proxyprotocol: pulumi.Bool(true),
				// TCP against the node port, which is what makes
				// externalTrafficPolicy: Local safe here. A node with no
				// ingress pod does not answer on it, fails the check, and is
				// taken out of rotation — so the client address survives
				// without an extra hop, and a node without a pod never
				// receives a request.
				HealthCheck: &hcloud.LoadBalancerServiceHealthCheckArgs{
					Protocol: pulumi.String("tcp"),
					Port:     pulumi.Int(service.nodePort),
					Interval: pulumi.Int(healthCheckInterval),
					Timeout:  pulumi.Int(healthCheckTimeout),
					Retries:  pulumi.Int(healthCheckRetries),
				},
			}, opts...); err != nil {
			return nil, fmt.Errorf("hcloud ingress load balancer service %d: %w",
				service.listenPort, err)
		}
	}

	// Every node in the cluster, not only the control plane.
	//
	// The load balancer forwards to a node port and the cluster's own
	// dataplane carries it the rest of the way, so any member is a valid
	// entry. Scoping this to a role would have to be revisited the first time
	// a worker pool exists, and the health check already removes a node that
	// cannot serve.
	if _, err := hcloud.NewLoadBalancerTarget(ctx, name+"-targets",
		&hcloud.LoadBalancerTargetArgs{
			LoadBalancerId: ingressID(loadBalancer),
			Type:           pulumi.String("label_selector"),
			LabelSelector:  pulumi.Sprintf("%s=%s", LabelCluster, args.ClusterName),
			UsePrivateIp:   pulumi.Bool(true),
		}, pulumiopts.With(opts, pulumi.DependsOn([]pulumi.Resource{attachment}))...); err != nil {
		return nil, fmt.Errorf("hcloud ingress load balancer target: %w", err)
	}

	return &IngressLoadBalancer{IPv4: loadBalancer.Ipv4, IPv6: loadBalancer.Ipv6}, nil
}

// IngressLoadBalancer is the load balancer's two public addresses.
//
// Both, because Hetzner gives every load balancer an IPv4 and an IPv6 and a
// dual-stack load balancer behind an A record alone is a half-answer: an
// IPv6-only client resolves nothing, which looks like the site being down
// rather than like a missing record.
type IngressLoadBalancer struct {
	IPv4 pulumi.StringOutput
	IPv6 pulumi.StringOutput
}

// ingressID is idToInt for a load balancer, named so the three call sites read
// as what they are rather than as a conversion.
func ingressID(loadBalancer *hcloud.LoadBalancer) pulumi.IntInput {
	return idToInt(loadBalancer.ID())
}
