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
//	pulumi stack init prod
//	pulumi config set --secret hcloud:token <token>
//	task cluster:image-bake        # once per Talos version
//	pulumi up
package main

import (
	"fmt"
	"os"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/clusterref"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/hetzner"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/pulumilog"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

func main() {
	pulumi.Run(func(ctx *pulumi.Context) error {
		// The topology comes from a committed file, not from stack config a
		// shell block wrote. `pulumi config` keeps only what must not be in
		// git — the Hetzner token — plus the few switches that are properties
		// of the operator's position rather than of the cluster.
		topologyPath := "cluster." + ctx.Stack() + ".yaml"

		topology, err := hetzner.LoadTopology(topologyPath)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("no topology for stack %q: create %s", ctx.Stack(), topologyPath)
			}

			return err
		}

		cfg := config.New(ctx, "hetzner-cluster")

		cluster, err := hetzner.NewCluster(ctx, topology.Metadata.Name, &hetzner.ClusterArgs{
			Topology:      topology,
			ImageSelector: cfg.Get("imageSelector"),
			PublicIPv4:    cfg.GetBool("publicIPv4"),
			AllowICMP:     cfg.GetBool("allowICMP"),
		})
		if err != nil {
			return err
		}

		// Output names come from pkg/clusterref, the same constants every layer
		// reads them back with. A rename is then a compile error in both
		// halves rather than a missing key at apply time.
		//
		// The version goes first because it is what every other output is
		// gated on: a layer reading any of them against a stack that predates
		// this gets one error naming this command, instead of one error per
		// output describing that output's own absence.
		ctx.Export(clusterref.OutputContractVersion, pulumi.Int(clusterref.ContractVersion))
		ctx.Export(clusterref.OutputKubeconfig, cluster.Kubeconfig)
		// Talosconfig is more powerful than the kubeconfig — it can reset nodes
		// and read etcd — so it is exported as a secret and used only by day-2
		// tasks.
		ctx.Export(clusterref.OutputTalosconfig, cluster.Talosconfig)
		ctx.Export(clusterref.OutputEndpoint, cluster.Endpoint)
		// Empty on a single control plane: no load balancer is created because
		// there is nothing to fail over between.
		ctx.Export(clusterref.OutputAPILoadBalancerIP, cluster.APILoadBalancerIP)
		// The CCM's route controller needs the network id to programme pod
		// routes inside the private network.
		ctx.Export(clusterref.OutputNetworkID, cluster.NetworkID)
		ctx.Export(clusterref.OutputPodCIDR, cluster.PodCIDR)
		ctx.Export(clusterref.OutputServiceCIDR, cluster.ServiceCIDR)
		// How many control-plane nodes exist, for layers sizing something that
		// cannot put two replicas on one node. Cilium's operator is one: its
		// replicas bind a host port, so the second stays Pending for ever on a
		// single-node cluster.
		ctx.Export(clusterref.OutputControlPlaneCount, pulumi.Int(topology.ControlPlane.Count))
		ctx.Export(clusterref.OutputClusterName, pulumi.String(topology.Metadata.Name))
		ctx.Export(clusterref.OutputLocation, pulumi.String(topology.Placement.Location))

		// The Hetzner token, re-exported for the layers that must call the
		// Hetzner API themselves — the cloud controller manager and the CSI
		// driver do. Without this each of them needs its own copy of the same
		// credential in its own stack config, which is the same secret typed
		// twice and rotated once.
		//
		// It travels the channel that already carries the cluster-admin
		// kubeconfig and the talosconfig, both strictly more powerful than an
		// API token, so this widens nothing. GetSecret marks it, and the typed
		// accessors in pkg/clusterref keep it marked.
		//
		// Exported unconditionally, empty when it is not in stack config. The
		// contract is total on purpose: a conditionally exported output makes
		// its own absence a state every consumer has to handle separately,
		// which is exactly what the version gate exists to abolish. A consumer
		// that needs the token reports the empty value with the remedy.
		//
		// `config.Get` only to test presence: reading it does not print it.
		log := pulumilog.New(ctx)
		token := pulumi.String("").ToStringOutput()

		if config.Get(ctx, "hcloud:token") == "" {
			log.Warn("hcloud-token",
				"not in stack config, so it is exported empty: layers needing it must set their own")
		} else {
			token = config.GetSecret(ctx, "hcloud:token")

			log.Done("hcloud-token", "exported for the layers that call the Hetzner API")
		}

		ctx.Export(clusterref.OutputHcloudToken, token)

		return nil
	})
}
