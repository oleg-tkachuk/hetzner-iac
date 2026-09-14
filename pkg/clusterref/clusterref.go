// Package clusterref is the contract between the cluster tier and the layers
// that deploy onto it.
//
// Layers are separate Pulumi projects so each can be applied on its own. What
// they share is this: the cluster tier exports a fixed set of output names,
// and a layer reads them through a StackReference. Naming those outputs in one
// place is what stops the contract from being a set of string literals
// scattered across five programs, where a rename in the producer fails at
// apply time in the consumer.
//
// # Why the contract is versioned
//
// A stack applied before an output existed simply does not have it, and a
// StackReference resolves an absent output as an error deep inside an apply.
// Handling that per output does not scale: with twelve of them a consumer
// faces four thousand partially-filled shapes, each needing its own fallback
// and its own message. Two outputs were added in one day and each grew its
// own, worded differently.
//
// So one output answers it for all of them. ContractVersion says which shape
// the producer speaks, every output is gated on it, and a stale cluster stack
// produces one error naming one command. Adding an output is then a version
// bump rather than a new fallback in every layer that reads the tier.
//
// The contract is also total: every declared output is always exported, empty
// if there is nothing to put in it. A conditionally exported output would make
// its own absence a state each consumer handles separately, which is the thing
// the gate exists to abolish.
//
// What this does not remove is the ordering: the tier must be applied before a
// layer can read what it publishes. That is inherent to keeping the layers
// independently appliable, and the only way out would be to merge the stacks.
// What it removes is having to reason about that ordering once per output.
package clusterref

import (
	"fmt"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// ContractVersion is the shape of the outputs below.
//
// Bump it in the same commit that adds, removes or repurposes an output.
// TestDeclared_ListsEveryOutputConstant pins the set, so a change to it fails
// until this is deliberate.
//
// v2 added nodeSubnet. Traefik accepts a PROXY protocol header only from
// addresses it is told to trust, and the Hetzner load balancer reaches the
// nodes over the private network — so the ingress layer needs the subnet the
// nodes sit in, and deriving it from a default would be a second copy of a
// value the topology already decides.
const ContractVersion = 2

// Output names exported by the cluster tier. Renaming one is a breaking change
// to every layer, which is why they are constants rather than literals.
const (
	OutputContractVersion   = "contractVersion"
	OutputKubeconfig        = "kubeconfig"
	OutputTalosconfig       = "talosconfig"
	OutputEndpoint          = "endpoint"
	OutputAPILoadBalancerIP = "apiLoadBalancerIp"
	OutputNetworkID         = "networkId"
	OutputNodeSubnet        = "nodeSubnet"
	OutputPodCIDR           = "podCidr"
	OutputServiceCIDR       = "serviceCidr"
	OutputClusterName       = "clusterName"
	OutputLocation          = "location"
	OutputHcloudToken       = "hcloudToken"
	OutputControlPlaneCount = "controlPlaneCount"
	OutputRoutingMode       = "routingMode"
)

// Declared is every output the cluster tier must export, in one list so the
// producer and the consumer cannot drift apart. A constant added above without
// a line here fails TestDeclared_ListsEveryOutputConstant; a line here the
// tier does not export fails infra/cluster's
// TestExports_CoverEveryDeclaredOutput. Either way a test rather than an
// apply.
var Declared = []string{
	OutputContractVersion,
	OutputKubeconfig,
	OutputTalosconfig,
	OutputEndpoint,
	OutputAPILoadBalancerIP,
	OutputNetworkID,
	OutputNodeSubnet,
	OutputPodCIDR,
	OutputServiceCIDR,
	OutputClusterName,
	OutputLocation,
	OutputHcloudToken,
	OutputControlPlaneCount,
	OutputRoutingMode,
}

// Cluster is the resolved view of the cluster tier's outputs.
//
// Every field is required. None is a pointer and none has a fallback: the
// version gate has already established that the producer speaks this shape, so
// a field missing anyway is a broken producer rather than an old one.
type Cluster struct {
	// Kubeconfig is cluster-admin. It stays a secret output all the way
	// through: a layer that logs it, or exports it again unwrapped, leaks it
	// into that layer's state.
	Kubeconfig pulumi.StringOutput
	Endpoint   pulumi.StringOutput
	NetworkID  pulumi.IntOutput

	// NodeSubnet is the private range the nodes are addressed in. A consumer
	// needs it to decide which source addresses to trust: the load balancer
	// reaches the nodes from inside this range, and Traefik rejects a PROXY
	// header from anywhere it is not told about.
	NodeSubnet pulumi.StringOutput

	PodCIDR     pulumi.StringOutput
	ServiceCIDR pulumi.StringOutput
	ClusterName pulumi.StringOutput
	Location    pulumi.StringOutput

	// HcloudToken is the Hetzner API token the cluster tier was built with,
	// re-exported so a layer that must call the Hetzner API does not need a
	// second copy of the same credential in its own config. It travels the
	// channel that already carries the kubeconfig and the talosconfig, both
	// strictly more powerful.
	//
	// Empty when the tier itself has no token in stack config — a cluster
	// built from an environment variable. A consumer that needs it says so.
	HcloudToken pulumi.StringOutput

	// RoutingMode is how the topology says pod traffic crosses nodes: native or
	// tunnel. The CNI layer needs it because Cilium is what implements the
	// choice, and the cluster tier is where it is declared — the two must agree
	// or the cluster has a datapath nobody configured.
	RoutingMode pulumi.StringOutput

	// ControlPlaneCount is how many control-plane nodes the topology declares.
	// A consumer needs it to size anything that cannot put two replicas on one
	// node — Cilium's operator binds a host port, so it is one.
	ControlPlaneCount pulumi.IntOutput

	// ContractCheck resolves to the producer's contract version, or fails with
	// the command that republishes it.
	//
	// A separate field rather than something every value is routed through,
	// and that distinction is the whole lesson of this type. Routing the
	// values through it put the check into the kubernetes provider's INPUTS,
	// so the provider's identity changed and Pulumi planned to replace it —
	// and with it every release and secret in every layer. On a live cluster
	// that is the CNI, the cloud controller manager and the CSI driver being
	// destroyed and recreated to improve an error message.
	//
	// pkg/layer exports it, which is what makes the engine await it: an output
	// nothing consumes is never resolved, so the error would never surface.
	ContractCheck pulumi.IntOutput
}

// Resolve reads the cluster tier's outputs from another stack.
//
// ref is a fully qualified stack name — <org>/<project>/<stack>, e.g.
// acme/hetzner-cluster/prod. It is configuration rather than a constant
// because the same layer code deploys against staging and production.
func Resolve(ctx *pulumi.Context, ref string) (*Cluster, error) {
	if ref == "" {
		return nil, fmt.Errorf("cluster stack reference is empty: set `clusterStackRef` to <org>/<project>/<stack>")
	}

	stack, err := pulumi.NewStackReference(ctx, ref, nil)
	if err != nil {
		return nil, fmt.Errorf("stack reference %q: %w", ref, err)
	}

	return &Cluster{
		// The check is a field, not a wrapper. See ContractCheck.
		ContractCheck: publishedVersion(stack, ref),

		// The typed accessors keep the secretness of the kubeconfig and the
		// token intact, which a manual ApplyT(string) round-trip would quietly
		// drop.
		Kubeconfig:        stack.GetStringOutput(pulumi.String(OutputKubeconfig)),
		Endpoint:          stack.GetStringOutput(pulumi.String(OutputEndpoint)),
		NetworkID:         stack.GetIntOutput(pulumi.String(OutputNetworkID)),
		NodeSubnet:        stack.GetStringOutput(pulumi.String(OutputNodeSubnet)),
		PodCIDR:           stack.GetStringOutput(pulumi.String(OutputPodCIDR)),
		ServiceCIDR:       stack.GetStringOutput(pulumi.String(OutputServiceCIDR)),
		ClusterName:       stack.GetStringOutput(pulumi.String(OutputClusterName)),
		Location:          stack.GetStringOutput(pulumi.String(OutputLocation)),
		HcloudToken:       stack.GetStringOutput(pulumi.String(OutputHcloudToken)),
		ControlPlaneCount: stack.GetIntOutput(pulumi.String(OutputControlPlaneCount)),
		RoutingMode:       stack.GetStringOutput(pulumi.String(OutputRoutingMode)),
	}, nil
}

// publishedVersion reads the producer's contract version, or fails with the
// command that publishes it.
//
// This is the one place an absent output is an ordinary state rather than a
// defect: a stack applied before versioning existed has no such output, which
// means version zero. Every other output can then be read with the typed
// accessor that fails an absent one, because this has already ruled it out.
func publishedVersion(stack *pulumi.StackReference, ref string) pulumi.IntOutput {
	return stack.GetOutput(pulumi.String(OutputContractVersion)).
		ApplyT(func(value any) (int, error) {
			published := 0

			switch typed := value.(type) {
			case nil:
			case float64:
				// Pulumi sends JSON numbers as float64.
				published = int(typed)
			case int:
				published = typed
			default:
				return 0, fmt.Errorf("stack reference output %q: expected a number, got %T",
					OutputContractVersion, value)
			}

			if published < ContractVersion {
				return 0, fmt.Errorf(
					"the cluster stack %s publishes contract v%d, and this layer needs v%d.\n"+
						"Apply the cluster tier so it republishes what the layers read:\n"+
						"  task cluster:apply stack=<stack>",
					ref, published, ContractVersion)
			}

			return published, nil
		}).(pulumi.IntOutput)
}
