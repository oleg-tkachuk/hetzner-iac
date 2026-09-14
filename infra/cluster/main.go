// Command hetzner-cluster provisions the Hetzner Cloud cluster tier.
//
// It is the only Pulumi project that talks to the Hetzner API. Everything
// under layers/ talks to Kubernetes, reading this stack's kubeconfig through a
// StackReference — which is what lets a layer be applied, previewed and
// destroyed without touching the cluster underneath it.
//
// Bring-up:
//
//	cd infra/cluster
//	cp cluster.example.yaml cluster.dev.yaml   # then edit adminCIDRs
//	pulumi stack init dev
//	pulumi config set --secret hcloud:token <token>
//	task cluster:image-bake        # once per Talos version
//	pulumi up
package main

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/clusterref"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/hetzner"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/pulumilog"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

// TokenConfigKey is where the Hetzner token lives in stack config. Namespaced
// to the provider, because that is the key the hcloud provider itself reads.
const TokenConfigKey = "hcloud:token"

func main() {
	pulumi.Run(program)
}

// topologyPath names the committed file that describes one stack's cluster.
func topologyPath(stack string) string {
	return "cluster." + stack + ".yaml"
}

// program is the cluster tier, separated from main so tests can run it under
// pulumi.WithMocks. Nothing here is reachable otherwise: a package main cannot
// be imported, so this program used to be the one Pulumi project checked by
// reading its source as text.
func program(ctx *pulumi.Context) error {
	// The topology comes from a committed file, not from stack config a
	// shell block wrote. `pulumi config` keeps only what must not be in
	// git — the Hetzner token — plus the few switches that are properties
	// of the operator's position rather than of the cluster.
	path := topologyPath(ctx.Stack())

	topology, err := hetzner.LoadTopology(path)
	if err != nil {
		// errors.Is, not os.IsNotExist: LoadTopology wraps the filesystem
		// error with %w, and os.IsNotExist does not walk a wrapped chain — it
		// unwraps only the os error types. With it, this branch never ran.
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("no topology for stack %q: create %s", ctx.Stack(), path)
		}

		return err
	}

	// Nothing shaping the cluster comes from `pulumi config` any more. The
	// topology is the whole per-environment description, so a cluster is
	// reviewable in a diff and reproducible from a clone — three switches
	// in stack config left somebody's shell as the only record of them.
	// Stack config keeps the token, because a token must not be in git.
	cluster, err := hetzner.NewCluster(ctx, topology.Metadata.Name, &hetzner.ClusterArgs{
		Topology:      topology,
		ImageSelector: topology.Talos.ImageSelector,
		PublicIPv4:    topology.PublicIPv4Enabled(),
		AllowICMP:     topology.ICMPAllowed(),
	})
	if err != nil {
		return err
	}

	for name, value := range exports(topology, cluster, clusterToken(ctx)) {
		ctx.Export(name, value)
	}

	return nil
}

// exports pairs every output name in the contract with the value this tier
// publishes for it.
//
// A map rather than a sequence of ctx.Export calls, because the names are only
// half the contract. Whether each name carries the right value was checked by
// reading this file as text, which cannot tell podCidr from nodeSubnet once
// both are strings — and a layer reads the value, not the name.
//
// Output names come from pkg/clusterref, the same constants every layer reads
// them back with. A rename is then a compile error in both halves rather than
// a missing key at apply time.
func exports(topology *hetzner.Topology, cluster *hetzner.Cluster, token pulumi.StringInput) map[string]pulumi.Input {
	return map[string]pulumi.Input{
		// Every other output is gated on the version: a layer reading any of
		// them against a stack that predates this gets one error naming the
		// command that republishes them, instead of one error per output
		// describing that output's own absence.
		clusterref.OutputContractVersion: pulumi.Int(clusterref.ContractVersion),

		clusterref.OutputKubeconfig: cluster.Kubeconfig,

		// Talosconfig is more powerful than the kubeconfig — it can reset
		// nodes and read etcd — so it is exported as a secret and used only by
		// the tasks that operate the cluster once it exists.
		clusterref.OutputTalosconfig: cluster.Talosconfig,

		clusterref.OutputEndpoint: cluster.Endpoint,

		// Empty on a single control plane: no load balancer is created because
		// there is nothing to fail over between.
		clusterref.OutputAPILoadBalancerIP: cluster.APILoadBalancerIP,

		// The CCM's route controller needs the network id to programme pod
		// routes inside the private network.
		clusterref.OutputNetworkID: cluster.NetworkID,

		// The private range the nodes are addressed in. The ingress layer
		// trusts PROXY protocol headers from inside it and nowhere else.
		clusterref.OutputNodeSubnet: pulumi.String(topology.Network.NodeSubnet),

		clusterref.OutputPodCIDR:     cluster.PodCIDR,
		clusterref.OutputServiceCIDR: cluster.ServiceCIDR,

		// How many control-plane nodes exist, for layers sizing something that
		// cannot put two replicas on one node. Cilium's operator is one: its
		// replicas bind a host port, so the second stays Pending for ever on a
		// single-node cluster.
		clusterref.OutputControlPlaneCount: pulumi.Int(topology.ControlPlane.Count),
		clusterref.OutputRoutingMode:       pulumi.String(topology.Network.RoutingMode),

		// Empty until the topology names one, and exported either way: the
		// contract is total, so a consumer reads an empty string rather than
		// handling an absent output.
		clusterref.OutputDomain:  pulumi.String(topology.Metadata.Domain),
		clusterref.OutputDNSZone: pulumi.String(topology.Metadata.DNSZone),

		clusterref.OutputClusterName: pulumi.String(topology.Metadata.Name),
		clusterref.OutputLocation:    pulumi.String(topology.Placement.Location),

		clusterref.OutputHcloudToken: token,
	}
}

// clusterToken is the Hetzner token as the contract publishes it: the secret
// from stack config, or empty when there is none.
//
// Re-exported for the layers that must call the Hetzner API themselves — the
// cloud controller manager and the CSI driver do. Without this each of them
// needs its own copy of the same credential in its own stack config, which is
// the same secret typed twice and rotated once.
//
// It travels the channel that already carries the cluster-admin kubeconfig and
// the talosconfig, both strictly more powerful than an API token, so this
// widens nothing. GetSecret marks it, and the typed accessors in pkg/clusterref
// keep it marked.
//
// Total on purpose: empty rather than absent. A conditionally exported output
// makes its own absence a state every consumer has to handle separately, which
// is exactly what the version gate exists to abolish. A consumer that needs
// the token reports the empty value with the remedy.
//
// `config.Get` only to test presence: reading it does not print it.
func clusterToken(ctx *pulumi.Context) pulumi.StringOutput {
	log := pulumilog.New(ctx)

	if config.Get(ctx, TokenConfigKey) == "" {
		log.Warn("hcloud-token",
			"not in stack config, so it is exported empty: layers needing it must set their own")

		return pulumi.String("").ToStringOutput()
	}

	log.Done("hcloud-token", "exported for the layers that call the Hetzner API")

	return config.GetSecret(ctx, TokenConfigKey)
}
