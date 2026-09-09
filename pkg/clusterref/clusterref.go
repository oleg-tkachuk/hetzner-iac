// Package clusterref is the contract between the cluster tier and the layers
// that deploy onto it.
//
// Layers are separate Pulumi projects so each can be applied on its own. What
// they share is this: the cluster tier exports a fixed set of output names,
// and a layer reads them through a StackReference. Naming those outputs in one
// place is what stops the contract from being a set of string literals
// scattered across five programs, where a rename in the producer fails at
// apply time in the consumer.
package clusterref

import (
	"fmt"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// Output names exported by the cluster tier. Renaming one is a breaking change
// to every layer, which is why they are constants rather than literals.
const (
	OutputKubeconfig        = "kubeconfig"
	OutputTalosconfig       = "talosconfig"
	OutputEndpoint          = "endpoint"
	OutputAPILoadBalancerIP = "apiLoadBalancerIp"
	OutputNetworkID         = "networkId"
	OutputPodCIDR           = "podCidr"
	OutputServiceCIDR       = "serviceCidr"
	OutputClusterName       = "clusterName"
	OutputLocation          = "location"
)

// Cluster is the resolved view of the cluster tier's outputs.
type Cluster struct {
	// Kubeconfig is cluster-admin. It stays a secret output all the way
	// through: a layer that logs it, or exports it again unwrapped, leaks it
	// into that layer's state.
	Kubeconfig  pulumi.StringOutput
	Endpoint    pulumi.StringOutput
	NetworkID   pulumi.IntOutput
	PodCIDR     pulumi.StringOutput
	ServiceCIDR pulumi.StringOutput
	ClusterName pulumi.StringOutput
	Location    pulumi.StringOutput
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
		// GetOutput returns pulumi.AnyOutput; the typed accessors below keep
		// the secretness of the kubeconfig intact, which a manual
		// ApplyT(string) round-trip would quietly drop.
		Kubeconfig:  stack.GetStringOutput(pulumi.String(OutputKubeconfig)),
		Endpoint:    stack.GetStringOutput(pulumi.String(OutputEndpoint)),
		NetworkID:   stack.GetIntOutput(pulumi.String(OutputNetworkID)),
		PodCIDR:     stack.GetStringOutput(pulumi.String(OutputPodCIDR)),
		ServiceCIDR: stack.GetStringOutput(pulumi.String(OutputServiceCIDR)),
		ClusterName: stack.GetStringOutput(pulumi.String(OutputClusterName)),
		Location:    stack.GetStringOutput(pulumi.String(OutputLocation)),
	}, nil
}
