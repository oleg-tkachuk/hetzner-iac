// Command ingress installs Traefik behind a Hetzner load balancer.
//
// The load balancer is created HERE, through the Hetzner provider, rather than
// by asking the cloud controller manager for one. Both were tried against a
// live cluster; internal/pkg/hetzner.NewIngressLoadBalancer records what the CCM route
// cost. In short: a CCM-managed load balancer is invisible to `plan` and
// `destroy`, and it refuses to target a control-plane node at all.
//
// The values themselves are internal/pkg/values/traefik.yaml.tmpl. What is left here is
// the part Go has to do: read stack config, resolve what the cluster tier
// published, and create the Hetzner resources the chart no longer asks for.
package main

import (
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/hetzner"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/layer"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/values"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// DefaultLoadBalancerType is the smallest Hetzner load balancer. It carries an
// ingress comfortably; the reason to change it is throughput, not
// availability.
const DefaultLoadBalancerType = "lb11"

// ControllerReplicas is how many Traefik pods to run. Two, so losing one node
// does not take ingress with it.
const ControllerReplicas = 2

// Chart is the registry key, which is also what the values template is named
// after.
const Chart = "traefik"

// Stack outputs. The addresses are the one fact an operator needs from this
// layer, and there is now somewhere to read them from: under the cloud
// controller manager they existed only on a Service's status, so `pulumi stack
// output` had nothing to say about how the cluster is reached.
const (
	OutputAddress     = "ingressIp"
	OutputAddressIPv6 = "ingressIpv6"
	OutputHostname    = "ingressHostname"
	OutputRecords     = "dnsRecords"
)

// RecordsPerDomain is how many RRSets one domain gets: an A and an AAAA.
const RecordsPerDomain = 2

// Components are what this layer deploys. One of them — what the table buys
// here is the enumeration: layertest asserts the chart is pinned and that
// internal/pkg/workloads knows what it produces.
var Components = layer.Components{
	{
		Chart: Chart,
		ValuesFrom: func(r *layer.Runner) pulumi.Output {
			return IngressData(r.Cluster.NodeSubnet)
		},
	},
}

// IngressData resolves the cluster tier's outputs into the template's data.
//
// Separated from renderValues so a test can assert what the template will be
// given without a Pulumi run.
func IngressData(nodeSubnet pulumi.StringInput) pulumi.Output {
	return pulumi.All(nodeSubnet).
		ApplyT(func(resolved []any) any {
			return values.Traefik{
				Replicas:   ControllerReplicas,
				NodeSubnet: resolved[0].(string),
				// The same two constants internal/pkg/hetzner points the load
				// balancer's services and health checks at.
				NodePortHTTP:  platform.IngressNodePortHTTP,
				NodePortHTTPS: platform.IngressNodePortHTTPS,
			}
		})
}

// deploy installs the chart, then creates the load balancer in front of it.
//
// The order is not a dependency and is not expressed as one. The load balancer
// health-checks a node port, so it converges on its own once Traefik is
// listening — and making it wait for the release would mean an ingress address
// that does not exist until a chart somewhere else is healthy.
func deploy(r *layer.Runner) error {
	if _, err := r.Deploy(Components); err != nil {
		return err
	}

	// A provider of its own, because this is the first layer to create
	// anything in Hetzner rather than in Kubernetes. r.Options carries the
	// Kubernetes provider, which this must not inherit.
	hcloudProvider, err := hetzner.NewProvider(r.Ctx, r.Cluster.HcloudToken)
	if err != nil {
		return err
	}

	// Said out loud, because this is the one billable resource a platform
	// layer creates and its size is a config decision. The three lines this
	// layer already prints are all about DNS, so the load balancer — the thing
	// that costs money every hour — was the quiet one.
	balancerType := r.StringOr("loadBalancerType", DefaultLoadBalancerType)
	// Type only: the location is the cluster's, and the cluster tier reports
	// it. Reading it here would mean resolving an Output to print a word.
	r.Log.Step("load-balancer", balancerType)

	balancer, err := hetzner.NewIngressLoadBalancer(r.Ctx, "ingress", hetzner.IngressLoadBalancerArgs{
		ClusterName:      r.Cluster.ClusterName,
		Location:         r.Cluster.Location,
		NetworkID:        r.Cluster.NetworkID,
		LoadBalancerType: balancerType,
	}, pulumi.Provider(hcloudProvider))
	if err != nil {
		return err
	}

	r.Ctx.Export(OutputAddress, balancer.IPv4)
	r.Ctx.Export(OutputAddressIPv6, balancer.IPv6)
	r.Ctx.Export(OutputHostname, r.Cluster.Domain)

	// Exported so the engine awaits it: the records are created inside an
	// apply, and an unconsumed output would swallow any error from it.
	r.Ctx.Export(OutputRecords, records(r, balancer, hcloudProvider))

	return nil
}

// records points the domain at the load balancer, when there is a domain and
// Hetzner holds its zone.
//
// Both halves are optional and mean different things, so both are logged. No
// domain is a new environment. A domain with no zone here is a domain hosted
// somewhere else, which is a supported arrangement — the records are then
// somebody else's to write, and this layer must not imply otherwise by staying
// silent.
//
// Returns a count rather than nothing, and the caller exports it. An output
// nothing consumes is never resolved, so an error inside this apply would
// never surface — the same reason internal/pkg/layer exports its contract check.
func records(
	r *layer.Runner,
	balancer *hetzner.IngressLoadBalancer,
	provider pulumi.ProviderResource,
) pulumi.IntOutput {
	return pulumi.All(r.Cluster.Domain, r.Cluster.DNSZone).
		ApplyT(func(resolved []any) (int, error) {
			domain, zone := resolved[0].(string), resolved[1].(string)

			switch {
			case domain == "":
				r.Log.Skipped("dns", "metadata.domain unset, no records and no Ingress anywhere")

				return 0, nil
			case zone == "":
				r.Log.Skipped("dns", "metadata.dnsZone unset, so "+domain+
					" is hosted elsewhere — point it at the ingressIp output by hand")

				return 0, nil
			}

			r.Log.Step("dns", domain+" in zone "+zone)

			if err := hetzner.NewIngressRecords(r.Ctx, "ingress", hetzner.IngressRecordsArgs{
				Zone:   zone,
				Domain: domain,
				IPv4:   balancer.IPv4,
				IPv6:   balancer.IPv6,
			}, pulumi.Provider(provider)); err != nil {
				return 0, err
			}

			// One A and one AAAA.
			return RecordsPerDomain, nil
		}).(pulumi.IntOutput)
}

func main() {
	layer.Run(deploy)
}
