// Command ingress installs Traefik behind a Hetzner load balancer.
//
// The load balancer is not created here. Traefik asks for a Service of type
// LoadBalancer and the hcloud cloud controller manager from
// layers/10-node-platform turns that into a real one — which is why this layer
// depends on that layer rather than on the Hetzner API.
package main

import (
	"github.com/oleg-tkachuk/hetzner-iac/pkg/chartsettings"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/layer"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// DefaultLoadBalancerType is the smallest Hetzner load balancer. It carries an
// ingress comfortably; the reason to change it is throughput, not
// availability.
const DefaultLoadBalancerType = "lb11"

// ControllerReplicas is how many Traefik pods to run. Two, so losing one node
// does not take ingress with it.
const ControllerReplicas = 2

// Components are what this layer deploys. One of them — what the table buys
// here is the enumeration: layertest asserts the chart is pinned and that
// pkg/workloads knows what it produces.
var Components = layer.Components{
	{
		Chart: "traefik",
		Values: func(r *layer.Runner) pulumi.Map {
			// The load balancer's name carries the cluster's, so two clusters
			// in one project do not collide on it.
			name := r.Cluster.ClusterName.ApplyT(func(cluster string) string {
				return cluster + "-ingress"
			}).(pulumi.StringOutput)

			return IngressValues(name, r.Cluster.Location, r.Cluster.NodeSubnet,
				r.StringOr("loadBalancerType", DefaultLoadBalancerType))
		},
	},
}

func main() {
	layer.Run(func(r *layer.Runner) error {
		_, err := r.Deploy(Components)

		return err
	})
}

// IngressValues builds the chart values.
//
// The annotations are read by the cloud controller manager, not by Traefik.
// That split is the thing to keep in mind here: a wrong annotation produces a
// load balancer that exists and routes to nothing, with no error from either
// component.
//
// PROXY protocol has to be set on BOTH sides — the annotation tells the load
// balancer to send the header, and each entry point has to be told which
// addresses may send it. Enable one alone and every request fails.
//
// nodeSubnet is that trust list, and it comes from the cluster tier rather
// than from a constant here: the load balancer reaches the nodes over the
// private network, so the header arrives from inside the range the topology
// assigns the nodes. Trusting a wider range would accept a spoofed header from
// any pod; trusting nothing is what Traefik does by default, and it rejects
// every connection that arrives through the load balancer.
func IngressValues(name, location, nodeSubnet pulumi.StringInput, loadBalancerType string) pulumi.Map {
	annotations := pulumi.Map{
		"load-balancer.hetzner.cloud/name":     name,
		"load-balancer.hetzner.cloud/location": location,
		"load-balancer.hetzner.cloud/type":     pulumi.String(loadBalancerType),
		// Reach the nodes over the private network. Targeting public
		// addresses would send traffic out of and back into Hetzner's
		// network — metered, slower, and needing firewall rules that would
		// otherwise not exist.
		"load-balancer.hetzner.cloud/use-private-ip":     pulumi.String("true"),
		"load-balancer.hetzner.cloud/uses-proxyprotocol": pulumi.String("true"),
	}

	// One trust list, both entry points. Written from the constants in
	// pkg/chartsettings so the render check can assert the same keys reach
	// Traefik's arguments — Helm accepts a misspelt key and leaves the list
	// empty, which is the failure this pair exists to catch.
	trusted := pulumi.Map{
		chartsettings.TraefikProxyProtocol: pulumi.Map{
			chartsettings.TraefikTrustedIPs: pulumi.StringArray{nodeSubnet},
		},
	}

	return pulumi.Map{
		"deployment": pulumi.Map{"replicas": pulumi.Int(ControllerReplicas)},

		"service": pulumi.Map{
			"annotations": annotations,
			"spec": pulumi.Map{
				// Local keeps the client address usable end to end by
				// avoiding the extra hop that would rewrite it.
				"externalTrafficPolicy": pulumi.String("Local"),
			},
		},

		chartsettings.TraefikPorts: pulumi.Map{
			chartsettings.TraefikEntryPointWeb: trusted,
			chartsettings.TraefikEntryPointTLS: trusted,
		},

		// Spread controllers across nodes so losing one node does not take
		// ingress with it. The selector is the label pair the chart actually
		// stamps on the pods — a selector matching nothing is a constraint
		// that silently does nothing.
		"topologySpreadConstraints": pulumi.Array{
			pulumi.Map{
				"maxSkew":           pulumi.Int(1),
				"topologyKey":       pulumi.String("kubernetes.io/hostname"),
				"whenUnsatisfiable": pulumi.String("ScheduleAnyway"),
				"labelSelector": pulumi.Map{
					"matchLabels": pulumi.Map{
						"app.kubernetes.io/name": pulumi.String("traefik"),
					},
				},
			},
		},

		"podDisruptionBudget": pulumi.Map{
			"enabled":      pulumi.Bool(true),
			"minAvailable": pulumi.Int(1),
		},

		// No forwardedHeaders trust list, deliberately. The client address
		// arrives in the PROXY header; trusting X-Forwarded-* as well would
		// accept a spoofed one. Traefik's default is to trust nobody, so this
		// is a default kept rather than a value set — restating it would
		// render identically.

		// No dashboard either: `api.insecure` stays at its default of false,
		// so nothing serves the API without authentication. Exposing it is an
		// IngressRoute plus an auth middleware, which belongs with whatever
		// decides who may see it.
	}
}
