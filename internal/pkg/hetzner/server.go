package hetzner

import (
	"fmt"

	"github.com/pulumi/pulumi-hcloud/sdk/go/hcloud"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumix"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/pulumiopts"
)

// serverSpec is everything needed to create one Talos node.
type serverSpec struct {
	name             string
	serverType       string
	location         string
	imageID          pulumi.StringInput
	networkID        pulumi.IntInput
	privateIP        string
	placementGroupID pulumi.IntPtrInput
	labels           map[string]string
	publicIPv4       bool

	// protect refuses a delete or a replacement of this node through Pulumi.
	// Set for control-plane nodes, where etcd's data is, and not for workers,
	// which are replaceable by design. See createControlPlaneNodes.
	protect bool

	// stableAddress gives the node an explicit Primary IP that outlives the
	// server. See newPrimaryIP.
	stableAddress bool
}

// The Primary IP fields hcloud spells.
const (
	primaryIPTypeIPv4     = "ipv4"
	primaryIPAssigneeType = "server"
)

// newPrimaryIP is the public address a control-plane node keeps across a
// replacement.
//
// A server's implicit address is deleted with it, and servers here are
// replaced delete-first, so a replaced control-plane node came back on a new
// address — and the kubeconfig and talosconfig name the first node's. An
// explicit Primary IP with autoDelete off survives the delete and is assigned
// to the new server. It is protected with the server: a teardown that takes
// one takes the other.
func newPrimaryIP(ctx *pulumi.Context, spec serverSpec, opts ...pulumi.ResourceOption) (*hcloud.PrimaryIp, error) {
	// The server assigns the address, through its public network, because the
	// address has to exist before the server it is created with. assigneeId is
	// therefore the server's to set: left to this resource, an address read
	// back as assigned plans an unassignment, which takes the node's public
	// address away.
	options := pulumiopts.With(opts, pulumi.IgnoreChanges([]string{"assigneeId"}))
	if spec.protect {
		options = pulumiopts.With(options, pulumi.Protect(true))
	}

	address, err := hcloud.NewPrimaryIp(ctx, spec.name+"-ipv4", &hcloud.PrimaryIpArgs{
		Name:         pulumi.String(spec.name),
		Type:         pulumi.String(primaryIPTypeIPv4),
		AssigneeType: pulumi.String(primaryIPAssigneeType),
		Location:     pulumi.String(spec.location),
		AutoDelete:   pulumi.Bool(false),
		Labels:       toStringMap(spec.labels),
	}, options...)
	if err != nil {
		return nil, fmt.Errorf("hcloud primary ip for %q: %w", spec.name, err)
	}

	return address, nil
}

// newServer creates one node.
//
// Three resource options carry real weight here:
//
//   - IgnoreChanges on the image. A re-baked Talos snapshot gets a new id, and
//     without this every node in the cluster would be REPLACED by an unrelated
//     `task cluster:image:bake`. Talos is upgraded in place with
//     `talosctl upgrade`,
//     which is what makes ignoring the field correct rather than merely
//     convenient.
//   - CustomTimeouts above the provider default. A Talos node boots into
//     maintenance mode and waits for configuration; the default create timeout
//     is generous but the delete path is not, and a stuck delete blocks the
//     whole stack.
//   - The private network attachment is declared as a child resource rather
//     than inline, so the address can be pinned deterministically.
func newServer(ctx *pulumi.Context, spec serverSpec, opts ...pulumi.ResourceOption) (*hcloud.Server, error) {
	if spec.name == "" || spec.serverType == "" || spec.location == "" {
		return nil, fmt.Errorf("server %q: name, serverType and location are required", spec.name)
	}

	publicNet := &hcloud.ServerPublicNetArgs{
		Ipv4Enabled: pulumi.Bool(spec.publicIPv4),
		// IPv6 is free on Hetzner and off here regardless: Talos would
		// advertise an address the rest of the platform is not configured for.
		Ipv6Enabled: pulumi.Bool(false),
	}

	if spec.publicIPv4 && spec.stableAddress {
		address, err := newPrimaryIP(ctx, spec, opts...)
		if err != nil {
			return nil, err
		}

		publicNet.Ipv4 = idToInt(address.ID())
	}

	args := &hcloud.ServerArgs{
		Name:       pulumi.String(spec.name),
		ServerType: pulumi.String(spec.serverType),
		Location:   pulumi.String(spec.location),
		Image:      spec.imageID,
		Labels:     toStringMap(spec.labels),
		PublicNets: hcloud.ServerPublicNetArray{publicNet},
		Networks: hcloud.ServerNetworkTypeArray{
			&hcloud.ServerNetworkTypeArgs{
				NetworkId: spec.networkID,
				Ip:        pulumi.String(spec.privateIP),
			},
		},
		// Talos ignores cloud-init; the image boots straight into maintenance
		// mode and waits for a machine configuration over the Talos API. There
		// is deliberately no UserData here.
		PlacementGroupId: spec.placementGroupID,
		// A rescue-booted node is how a Talos image is baked, and leaving
		// rescue enabled would let a reboot land somewhere unexpected.
		Rescue: nil,
	}

	options := append([]pulumi.ResourceOption{
		pulumi.IgnoreChanges([]string{"image"}),
		// Delete the old server before creating its replacement.
		//
		// Pulumi's default is the opposite, and for a Hetzner server the
		// default cannot work: a name is unique within the project, so the
		// create half of create-before-delete is rejected before the delete
		// half runs —
		//
		//     server name is already used (uniqueness_error)
		//     creating replacement … **creating failed**
		//
		// which leaves the stack errored and the old server still standing.
		// Every input that forces a replacement hits this: the server type,
		// the datacenter, the private address. Measured on a deliberate
		// replacement of the only control-plane node, which failed in five
		// seconds having changed nothing.
		//
		// The cost is honest and unavoidable: the node is gone between the
		// delete and the create. On a single-node cluster that is an outage
		// either way.
		//
		// This comment used to add that on an HA cluster "the replacement is
		// one member at a time, which etcd survives". Nothing enforced that
		// and it is not true — the nodes have no dependency on each other and
		// `--parallel` defaults to 56 — which is why control-plane nodes now
		// carry spec.protect.
		pulumi.DeleteBeforeReplace(true),
		pulumi.Timeouts(&pulumi.CustomTimeouts{
			Create: "10m",
			Update: "10m",
			Delete: "15m",
		}),
	}, opts...)

	if spec.protect {
		options = pulumiopts.With(options, pulumi.Protect(true))
	}

	// The public network is read at create — which a replacement is — and
	// never updated. The provider updates it by powering the server off,
	// unassigning its address and, when the old state named no address id,
	// DELETING that address before assigning the new one. Measured on dev:
	// moving a node from its implicit address to the same address made
	// explicit powered it off and tried to delete the address it was given.
	if spec.stableAddress {
		options = pulumiopts.With(options, pulumi.IgnoreChanges([]string{"publicNets"}))
	}

	server, err := hcloud.NewServer(ctx, spec.name, args, options...)
	if err != nil {
		return nil, fmt.Errorf("hcloud server %q: %w", spec.name, err)
	}

	return server, nil
}

// nodeAddress is the address talosctl and the kubeconfig resource talk to.
//
// On a node with a public IPv4 that is the public address, because the host
// running the apply is outside the private network. On a private-only node it
// is the private address, which then requires the apply to run from inside the
// network — a deliberate trade, not an accident.
func nodeAddress(server *hcloud.Server, publicIPv4 bool, privateIP string) pulumi.StringOutput {
	if !publicIPv4 {
		return pulumi.String(privateIP).ToStringOutput()
	}

	return pulumix.Cast[pulumi.StringOutput](pulumix.ApplyErr(server.Ipv4Address,
		func(address string) (string, error) {
			if address == "" {
				return "", fmt.Errorf("server has no public IPv4 address, but one was requested")
			}

			return address, nil
		}))
}
