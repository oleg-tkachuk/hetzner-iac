// Command ingress installs ingress-nginx behind a Hetzner load balancer.
//
// The load balancer is not created here. ingress-nginx asks for a Service of
// type LoadBalancer and the hcloud cloud controller manager from
// 20-cloud-integration turns that into a real one — which is why this layer
// depends on that layer rather than on the Hetzner API.
package main

import (
	"github.com/oleg-tkachuk/hetzner-iac/pkg/chartsettings"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/layer"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

// DefaultLoadBalancerType is the smallest Hetzner load balancer. It carries a
// production ingress comfortably; the reason to change it is throughput, not
// availability.
const DefaultLoadBalancerType = "lb11"

func main() {
	layer.Run(func(r *layer.Runner) error {
		cfg := config.New(r.Ctx, "ingress")

		loadBalancerType := cfg.Get("loadBalancerType")
		if loadBalancerType == "" {
			loadBalancerType = DefaultLoadBalancerType
		}

		name := r.Cluster.ClusterName.ApplyT(func(cluster string) string {
			return cluster + "-ingress"
		}).(pulumi.StringOutput)

		if _, err := r.Release(r.Ctx, layer.ReleaseArgs{
			Chart:  "ingress-nginx",
			Values: IngressValues(name, r.Cluster.Location, loadBalancerType),
		}); err != nil {
			return err
		}

		return nil
	})
}

// IngressValues builds the chart values.
//
// The annotations are read by the cloud controller manager, not by
// ingress-nginx. That split is the thing to keep in mind here: a wrong
// annotation produces a load balancer that exists and routes to nothing, with
// no error from either component.
//
// PROXY protocol has to be set on BOTH sides — the annotation tells the load
// balancer to send the header, the controller config tells nginx to expect it.
// Enable one alone and every request fails to parse.
func IngressValues(name pulumi.StringInput, location pulumi.StringInput, loadBalancerType string) pulumi.Map {
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

	return pulumi.Map{
		"controller": pulumi.Map{
			"replicaCount": pulumi.Int(2),
			"service": pulumi.Map{
				"type":        pulumi.String("LoadBalancer"),
				"annotations": annotations,
				// Local keeps the client address usable end to end by
				// avoiding the extra hop that would rewrite it.
				"externalTrafficPolicy": pulumi.String("Local"),
			},
			"config": pulumi.Map{
				// Constants, not literals: a typo in either key is accepted by
				// Helm and silently leaves the default, which here means every
				// request arrives unparseable. task charts:render-check
				// asserts their effect using the same constants.
				chartsettings.IngressUseProxyProtocol: pulumi.String("true"),
				// The real client address arrives in the PROXY header. Trusting
				// a forwarded header as well would accept a spoofed one.
				chartsettings.IngressUseForwardedHeaders: pulumi.String("false"),
			},
			"metrics": pulumi.Map{
				"enabled": pulumi.Bool(true),
				// Owned by 60-observability, which installs the Prometheus
				// operator CRDs.
				"serviceMonitor": pulumi.Map{"enabled": pulumi.Bool(false)},
			},
			// Spread controllers across nodes so losing one node does not
			// take ingress with it.
			"topologySpreadConstraints": pulumi.Array{
				pulumi.Map{
					"maxSkew":           pulumi.Int(1),
					"topologyKey":       pulumi.String("kubernetes.io/hostname"),
					"whenUnsatisfiable": pulumi.String("ScheduleAnyway"),
					"labelSelector": pulumi.Map{
						"matchLabels": pulumi.Map{
							"app.kubernetes.io/name":      pulumi.String("ingress-nginx"),
							"app.kubernetes.io/component": pulumi.String("controller"),
						},
					},
				},
			},
			"podDisruptionBudget": pulumi.Map{
				"enabled":      pulumi.Bool(true),
				"minAvailable": pulumi.Int(1),
			},
			// Snippet annotations let any namespace inject nginx
			// configuration, which is a privilege-escalation path out of a
			// tenant namespace.
			"allowSnippetAnnotations": pulumi.Bool(false),
		},
	}
}
