package hetzner

import (
	"fmt"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"

	"github.com/pulumi/pulumi-hcloud/sdk/go/hcloud"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// Network is the private network every cluster node joins.
//
// All Kubernetes traffic — kubelet, etcd peers, CNI, the CCM's load-balancer
// targets — is pinned to this network rather than the public NIC. It is
// unmetered, not reachable from the internet, and, unlike the public
// interface, never filtered by a Hetzner firewall, so node-to-node ports need
// no rule maintenance as components are added.
type Network struct {
	pulumi.ResourceState

	// Network is the underlying resource; pass it as a parent or dependency.
	Network *hcloud.Network `pulumi:"-"`
	// Subnet must exist before any server joins, so servers depend on it
	// rather than on the network.
	Subnet *hcloud.NetworkSubnet `pulumi:"-"`

	NetworkID  pulumi.IntOutput    `pulumi:"networkId"`
	IPRange    pulumi.StringOutput `pulumi:"ipRange"`
	NodeSubnet pulumi.StringOutput `pulumi:"nodeSubnet"`
	GatewayIP  pulumi.StringOutput `pulumi:"gatewayIp"`
}

// NetworkArgs configures the private network.
type NetworkArgs struct {
	// ClusterName scopes the resource names and labels.
	ClusterName string
	// IPRange is the whole private network, e.g. 10.0.0.0/16.
	IPRange string
	// NodeSubnet is the subnet inside IPRange that nodes address from.
	NodeSubnet string
	// NetworkZone must contain the location the servers are created in.
	NetworkZone string
}

// NewNetwork provisions the private network and its node subnet.
func NewNetwork(ctx *pulumi.Context, name string, args *NetworkArgs, opts ...pulumi.ResourceOption) (*Network, error) {
	if args == nil {
		return nil, fmt.Errorf("NewNetwork(%s): args must not be nil", name)
	}

	component := &Network{}
	if err := ctx.RegisterComponentResource(typeNetwork, name, component, opts...); err != nil {
		return nil, fmt.Errorf("register %s: %w", typeNetwork, err)
	}

	// Every child is parented to the component, which is what makes the
	// component a real node in the resource graph: `pulumi up` shows one
	// collapsible tree per cluster concern rather than a flat list.
	parent := pulumi.Parent(component)

	labels := clusterspec.ResourceLabels(args.ClusterName, nil)

	network, err := hcloud.NewNetwork(ctx, name, &hcloud.NetworkArgs{
		Name:    pulumi.String(args.ClusterName),
		IpRange: pulumi.String(args.IPRange),
		Labels:  toStringMap(labels),
	}, parent)
	if err != nil {
		return nil, fmt.Errorf("hcloud network: %w", err)
	}

	subnet, err := hcloud.NewNetworkSubnet(ctx, name+"-subnet", &hcloud.NetworkSubnetArgs{
		NetworkId:   idToInt(network.ID()),
		Type:        pulumi.String("cloud"),
		NetworkZone: pulumi.String(args.NetworkZone),
		IpRange:     pulumi.String(args.NodeSubnet),
	}, pulumi.Parent(network),
		// Hetzner refuses to delete a subnet while servers hold addresses in
		// it. Replacing the old one first is the only ordering that works.
		pulumi.DeleteBeforeReplace(true))
	if err != nil {
		return nil, fmt.Errorf("hcloud network subnet: %w", err)
	}

	addressing, err := clusterspec.NewAddressing(args.NodeSubnet, clusterspec.PoolAddressStride)
	if err != nil {
		return nil, err
	}

	gateway, err := addressing.Gateway()
	if err != nil {
		return nil, err
	}

	component.Network = network
	component.Subnet = subnet
	component.NetworkID = idToInt(network.ID())
	component.IPRange = pulumi.String(args.IPRange).ToStringOutput()
	component.NodeSubnet = pulumi.String(args.NodeSubnet).ToStringOutput()
	component.GatewayIP = pulumi.String(gateway).ToStringOutput()

	if err := ctx.RegisterResourceOutputs(component, pulumi.Map{
		"networkId":  component.NetworkID,
		"ipRange":    component.IPRange,
		"nodeSubnet": component.NodeSubnet,
		"gatewayIp":  component.GatewayIP,
	}); err != nil {
		return nil, fmt.Errorf("register network outputs: %w", err)
	}

	return component, nil
}
