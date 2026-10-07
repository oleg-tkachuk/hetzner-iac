package hetzner

import (
	"fmt"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
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
	// IPv4ID and IPv6ID are the Primary IPs the cluster tier keeps for the
	// ingress. See NewIngressAddresses.
	IPv4ID pulumi.IntInput
	IPv6ID pulumi.IntInput
}

// Primary IP types, as hcloud spells them.
const (
	primaryIPTypeIPv4 = "ipv4"
	primaryIPTypeIPv6 = "ipv6"
)

// Fields of hcloud's LoadBalancer and PrimaryIp that the options below name.
// Pulumi matches IgnoreChanges against these spellings, and a misspelt one is
// ignored without a word.
const (
	fieldIPv4ID       = "ipv4Id"
	fieldIPv6ID       = "ipv6Id"
	fieldAssigneeID   = "assigneeId"
	fieldAssigneeType = "assigneeType"
)

// IngressAddresses are the ingress load balancer's public addresses, by
// Primary IP id.
type IngressAddresses struct {
	IPv4ID pulumi.IntOutput
	IPv6ID pulumi.IntOutput
}

// NewIngressAddresses creates the public addresses the ingress load balancer
// is created on.
//
// They live in the cluster tier rather than beside the load balancer in
// 40-ingress, so they outlive it. A load balancer's own addresses go with it,
// and the domain's records name them — so a destroyed and re-applied ingress
// layer used to come back on new addresses, and records hosted outside Hetzner
// went on pointing at the old ones with nothing saying so. Created here, with
// autoDelete off and Protect, a replaced load balancer is created on the same
// two addresses.
//
// The load balancer assigns them, at creation: Hetzner cannot reassign a load
// balancer's Primary IP afterwards. assigneeId and assigneeType are therefore
// the load balancer's, and left to this resource a read-back assignment would
// plan an unassignment — the same trap newPrimaryIP avoids for servers.
func NewIngressAddresses(
	ctx *pulumi.Context,
	name string,
	topology *clusterspec.Topology,
	opts ...pulumi.ResourceOption,
) (IngressAddresses, error) {
	options := pulumiopts.With(opts,
		pulumi.Protect(true),
		pulumi.IgnoreChanges([]string{fieldAssigneeID, fieldAssigneeType}))

	kinds := []string{primaryIPTypeIPv4, primaryIPTypeIPv6}
	ids := make(map[string]pulumi.IntOutput, len(kinds))

	for _, kind := range kinds {
		address, err := hcloud.NewPrimaryIp(ctx, name+"-ingress-"+kind, &hcloud.PrimaryIpArgs{
			Name:       pulumi.Sprintf("%s-ingress-%s", topology.Metadata.Name, kind),
			Type:       pulumi.String(kind),
			Location:   pulumi.String(topology.Placement.Location),
			AutoDelete: pulumi.Bool(false),
			Labels:     toStringMap(clusterspec.ResourceLabels(topology.Metadata.Name, nil)),
		}, options...)
		if err != nil {
			return IngressAddresses{}, fmt.Errorf("hcloud ingress %s primary ip: %w", kind, err)
		}

		ids[kind] = idToInt(address.ID())
	}

	return IngressAddresses{IPv4ID: ids[primaryIPTypeIPv4], IPv6ID: ids[primaryIPTypeIPv6]}, nil
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
) (*hcloud.LoadBalancer, error) {
	labels := pulumi.StringMap{
		clusterspec.LabelCluster:   args.ClusterName,
		clusterspec.LabelManagedBy: pulumi.String(clusterspec.ManagedBy),
	}

	// The addresses are taken at creation only, and ignored afterwards.
	// Hetzner cannot move a load balancer onto another Primary IP, so a
	// changed id could only ever plan a replacement — and state written before
	// these fields existed holds no id at all, so without this every live load
	// balancer would be planned for one.
	loadBalancer, err := hcloud.NewLoadBalancer(ctx, name, &hcloud.LoadBalancerArgs{
		Name:             pulumi.Sprintf("%s-ingress", args.ClusterName),
		LoadBalancerType: pulumi.String(args.LoadBalancerType),
		Location:         args.Location,
		Labels:           labels,
		Ipv4Id:           args.IPv4ID,
		Ipv6Id:           args.IPv6ID,
	}, pulumiopts.With(opts, pulumi.IgnoreChanges([]string{fieldIPv4ID, fieldIPv6ID}))...)
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
				// list in internal/pkg/charts/traefik.yaml.tmpl: Traefik trusts the
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
			LabelSelector:  pulumi.Sprintf("%s=%s", clusterspec.LabelCluster, args.ClusterName),
			UsePrivateIp:   pulumi.Bool(true),
		}, pulumiopts.With(opts, pulumi.DependsOn([]pulumi.Resource{attachment}))...); err != nil {
		return nil, fmt.Errorf("hcloud ingress load balancer target: %w", err)
	}

	// The resource itself, not a copy of two of its fields.
	//
	// It used to return an IngressLoadBalancer holding Ipv4 and Ipv6, which
	// read as a component and was not one: no ResourceState, so it did not
	// satisfy pulumi.Resource and a layer.Components entry could not return
	// it. That is why this layer's Hetzner half sat outside the table. The
	// hcloud resource carries both addresses already.
	return loadBalancer, nil
}

// ingressID is idToInt for a load balancer, named so its call sites read as
// what they are rather than as a conversion.
//
// Two of the three fields that take this load balancer's id want an int and
// the third wants a string: hcloud's LoadBalancerService spells it
// differently from LoadBalancerNetwork and LoadBalancerTarget, which is why
// one call above converts by hand instead.
func ingressID(loadBalancer *hcloud.LoadBalancer) pulumi.IntInput {
	return idToInt(loadBalancer.ID())
}
