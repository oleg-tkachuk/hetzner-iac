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
//	task cluster:image:bake        # once per Talos version
//	pulumi up
package main

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterref"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/hetzner"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/pulumilog"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

// The token's config key is internal/pkg/hetzner's, not this file's.
//
// It was a second `const TokenConfigKey = "hcloud:token"` here. Three things
// read that key — `task cluster:token` writes it, this tier reads it to
// re-export, and pkg/hetzner.Token reads it for every tool that calls the
// Hetzner API — and two of them were reading their own copy of the name. A
// rename in one would have left this tier exporting an empty token while the
// tools still found the value, which reads as the tier being broken.

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
	report(pulumilog.New(ctx), topology)

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

// report says out loud what the topology decided.
//
// The layers each narrate their own decisions and this tier narrated nothing,
// which is the wrong way round: Pulumi prints every resource it creates, so
// the ACTIONS were always visible, and what was not is the handful of choices
// derived from the topology. They are the expensive ones — a cluster where
// nothing can be scheduled, or an API with no load balancer in front of three
// nodes, is a cluster that comes up and then puzzles somebody.
//
// Derived from the same predicates NewCluster uses, so the report cannot
// describe a cluster other than the one being built.
func report(log *pulumilog.Logger, topology *hetzner.Topology) {
	workers := topology.TotalWorkers()

	log.Step("control-plane", fmt.Sprintf("%d node(s) of %s in %s",
		topology.ControlPlane.Count, topology.ControlPlane.ServerType, topology.Placement.Location))

	if topology.APILoadBalanced() {
		log.Step("api", "reached through a load balancer of type "+topology.ControlPlane.APILoadBalancerType)
	} else {
		log.Skipped("api", "one control-plane node, so it is its own endpoint and no load balancer is created")
	}

	if workers == 0 {
		// Not a warning: it is the supported shape for a small cluster, and
		// the machine config says so. But it is the difference between "no
		// pod can run" and "pods run on the control plane", and nothing else
		// in the output mentions it.
		log.Step("scheduling", "no worker pools, so workloads are allowed on the control plane")
	} else {
		log.Step("workers", fmt.Sprintf("%d node(s) across %d pool(s)", workers, len(topology.WorkerPools)))
	}

	// The EFFECTIVE selector, not the configured one. An empty imageSelector
	// is the ordinary case — lookupTalosImage falls back to the labels
	// cluster:image:bake stamps — and printing the empty string left the line
	// reading "image selected by " with nothing after it, which is how this
	// was found on the first preview.
	selector := topology.Talos.ImageSelector
	if selector == "" {
		selector = hetzner.TalosImageSelector(topology.Talos.Version)
	}

	log.Step("talos", topology.Talos.Version+" "+topology.Talos.Architecture+
		", image matching "+selector)

	if !topology.PublicIPv4Enabled() {
		// Worth a warning rather than a step: every task that reaches a node
		// over talosctl or SSH stops working, and the cause is one topology
		// field away from the symptom.
		log.Warn("addressing", "publicIPv4 is off — nodes have no routable address, "+
			"so talosctl reaches them only from inside the network")
	}
}

// exports pairs every output name in the contract with the value this tier
// publishes for it.
//
// A map rather than a sequence of ctx.Export calls, because the names are only
// half the contract. Whether each name carries the right value was checked by
// reading this file as text, which cannot tell podCidr from nodeSubnet once
// both are strings — and a layer reads the value, not the name.
//
// Output names come from internal/pkg/clusterref, the same constants every layer reads
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
// widens nothing. GetSecret marks it, and the typed accessors in internal/pkg/clusterref
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

	if config.Get(ctx, hetzner.TokenConfigKey) == "" {
		log.Warn("hcloud-token",
			"not in stack config, so it is exported empty: layers needing it must set their own")

		return pulumi.String("").ToStringOutput()
	}

	log.Done("hcloud-token", "exported for the layers that call the Hetzner API")

	return config.GetSecret(ctx, hetzner.TokenConfigKey)
}
