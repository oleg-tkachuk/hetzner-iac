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
	"fmt"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/charts"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/hetzner"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/layer"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/values"

	"github.com/pulumi/pulumi-hcloud/sdk/go/hcloud"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumix"
)

// DefaultLoadBalancerType is the smallest Hetzner load balancer. It carries an
// ingress comfortably; the reason to change it is throughput, not
// availability.
const DefaultLoadBalancerType = "lb11"

// ControllerReplicas is how many Traefik pods to run. Two, so losing one node
// does not take ingress with it.
const ControllerReplicas = 2

// Chart is the registry key, which is also what the values template is named
// after. From the chart's own declaration rather than spelled again here.
const Chart = charts.Traefik

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

// BalancerComponent is the load balancer's name in the set, and what the
// exports below read it back by.
const BalancerComponent = "load-balancer"

// components is what this layer deploys.
//
// A function rather than a var because one of them needs the Hetzner provider,
// which exists only once the cluster tier's token has been read. The provider
// is created once in deploy and closed over, rather than per component: two
// providers would be two resources in state for one credential.
//
// The load balancer is IN the table now. It used to be created by hand after
// r.Deploy returned, which left half of what this layer makes outside the
// enumeration layertest checks — and the half in question is the billable one.
//
// It carries no After, and that absence is the point. The load balancer
// health-checks a node port, so it converges on its own once Traefik is
// listening; making it wait for the release would mean an ingress address that
// does not exist until a chart somewhere else is healthy. Written as a missing
// After rather than as a paragraph, the table now says so itself.
func components(provider pulumi.ProviderResource) layer.Components {
	return layer.Components{
		{
			Chart: Chart,
			ValuesFrom: func(r *layer.Runner) pulumi.Output {
				return IngressData(r.Cluster.NodeSubnet)
			},
		},
		{
			Name:   BalancerComponent,
			Create: createBalancer(provider),
		},
	}
}

// createBalancer puts the load balancer in front of the node ports.
func createBalancer(provider pulumi.ProviderResource) layer.CreateFunc {
	return func(r *layer.Runner, dependencies []pulumi.Resource) (pulumi.Resource, error) {
		// Said out loud, because this is the one billable resource a platform
		// layer creates and its size is a config decision.
		balancerType := r.StringOr("loadBalancerType", DefaultLoadBalancerType)
		// Type only: the location is the cluster's, and the cluster tier
		// reports it. Reading it here would mean resolving an Output to print
		// a word.
		r.Log.Step(BalancerComponent, balancerType)

		return hetzner.NewIngressLoadBalancer(r.Ctx, "ingress", hetzner.IngressLoadBalancerArgs{
			ClusterName:      r.Cluster.ClusterName,
			Location:         r.Cluster.Location,
			NetworkID:        r.Cluster.NetworkID,
			LoadBalancerType: balancerType,
		}, append(layer.DependsOn(dependencies), pulumi.Provider(provider))...)
	}
}

// IngressData resolves the cluster tier's outputs into the template's data.
//
// Separated from renderValues so a test can assert what the template will be
// given without a Pulumi run.
func IngressData(nodeSubnet pulumi.StringInput) pulumi.Output {
	// One input, so no pulumi.All: it took the typed output, put it in a []any
	// and handed it back to be recovered by index and asserted.
	return pulumix.Apply(nodeSubnet.ToStringOutput(),
		func(subnet string) any {
			return values.Traefik{
				Replicas:   ControllerReplicas,
				NodeSubnet: subnet,
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
	// A provider of its own, because this is the first layer to create
	// anything in Hetzner rather than in Kubernetes. r.Options carries the
	// Kubernetes provider, which this must not inherit.
	hcloudProvider, err := hetzner.NewProvider(r.Ctx, r.Cluster.HcloudToken)
	if err != nil {
		return err
	}

	deployed, err := r.Deploy(components(hcloudProvider))
	if err != nil {
		return err
	}

	// Read back out of the set, because the exports below need the addresses
	// and Deployed holds resources. The assertion is the price of putting the
	// load balancer in the table, and it is worth paying: what this layer
	// creates is now enumerable.
	balancer, ok := deployed[BalancerComponent].(*hcloud.LoadBalancer)
	if !ok {
		return fmt.Errorf("%s was not created as a load balancer", BalancerComponent)
	}

	r.Ctx.Export(OutputAddress, balancer.Ipv4)
	r.Ctx.Export(OutputAddressIPv6, balancer.Ipv6)
	r.Ctx.Export(OutputHostname, r.Cluster.Domain)

	// Exported so the engine awaits it: the records are created inside an
	// apply, and an unconsumed output would swallow any error from it.
	r.Ctx.Export(OutputRecords, records(r, balancer, hcloudProvider))

	return nil
}

// records points the domain at the load balancer, when there is a domain and
// Hetzner holds its zone.
//
// NOT a component, and that is a limit of the table rather than an oversight.
// A component's membership is decided before anything resolves — order() runs
// first, and Create is called for every entry — so a component can decline on
// CONFIG the way 50-gitops's root does. These records exist only if two
// StackReference outputs say so, and those are known inside an apply and
// nowhere earlier. NewIngressRecords also hands back no resource for a
// component to return, because it looks the zone up and writes two RRSets
// inside that same apply.
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
	balancer *hcloud.LoadBalancer,
	provider pulumi.ProviderResource,
) pulumi.IntOutput {
	return pulumix.Cast[pulumi.IntOutput](pulumix.Apply2Err(
		r.Cluster.Domain, r.Cluster.DNSZone,
		func(domain, zone string) (int, error) {
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
				IPv4:   balancer.Ipv4,
				IPv6:   balancer.Ipv6,
			}, pulumi.Provider(provider)); err != nil {
				return 0, err
			}

			// One A and one AAAA.
			return RecordsPerDomain, nil
		}))
}

func main() {
	layer.Run(deploy)
}
