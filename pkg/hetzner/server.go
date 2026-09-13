package hetzner

import (
	"fmt"

	"github.com/pulumi/pulumi-hcloud/sdk/go/hcloud"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumix"
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
}

// newServer creates one node.
//
// Three resource options carry real weight here:
//
//   - IgnoreChanges on the image. A re-baked Talos snapshot gets a new id, and
//     without this every node in the cluster would be REPLACED by an unrelated
//     `task image-bake`. Talos is upgraded in place with `talosctl upgrade`,
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
		// either way, and on an HA one the replacement is one member at a
		// time, which etcd survives.
		pulumi.DeleteBeforeReplace(true),
		pulumi.Timeouts(&pulumi.CustomTimeouts{
			Create: "10m",
			Update: "10m",
			Delete: "15m",
		}),
	}, opts...)

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
