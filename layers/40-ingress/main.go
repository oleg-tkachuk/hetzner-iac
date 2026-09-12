// Command ingress installs Traefik behind a Hetzner load balancer.
//
// The load balancer is not created here. Traefik asks for a Service of type
// LoadBalancer and the hcloud cloud controller manager from
// layers/10-node-platform turns that into a real one — which is why this layer
// depends on that layer rather than on the Hetzner API.
//
// The values themselves are pkg/values/traefik.yaml.tmpl. What is left here is
// the part Go has to do: read stack config, and resolve what the cluster tier
// published before the template can be executed against it.
package main

import (
	"github.com/oleg-tkachuk/hetzner-iac/pkg/layer"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/values"

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

// Ingress is what pkg/values/traefik.yaml.tmpl is executed against.
//
// A struct rather than a map so a field the template names and the data does
// not have fails the render, instead of quietly leaving an empty string in a
// values file.
type Ingress struct {
	// Replicas is the controller count.
	Replicas int

	// Name and Location are what the cloud controller manager builds the load
	// balancer from.
	Name     string
	Location string

	// LoadBalancerType is the Hetzner type, from stack config.
	LoadBalancerType string

	// NodeSubnet is the range Traefik trusts a PROXY protocol header from. It
	// comes from the cluster tier rather than a constant here: the load
	// balancer reaches the nodes privately, so the header arrives from inside
	// the range the topology assigns.
	NodeSubnet string
}

// Components are what this layer deploys. One of them — what the table buys
// here is the enumeration: layertest asserts the chart is pinned and that
// pkg/workloads knows what it produces.
var Components = layer.Components{
	{
		Chart:      Chart,
		ValuesYAML: renderValues,
	},
}

// renderValues resolves what the template needs, then renders it.
func renderValues(r *layer.Runner) pulumi.AssetOrArchiveArrayInput {
	loadBalancerType := r.StringOr("loadBalancerType", DefaultLoadBalancerType)

	return values.Asset(Chart, IngressData(
		r.Cluster.ClusterName, r.Cluster.Location, r.Cluster.NodeSubnet, loadBalancerType))
}

// IngressData resolves the cluster tier's outputs into the template's data.
//
// Separated from renderValues so a test can assert what the template will be
// given without a Pulumi run.
func IngressData(clusterName, location, nodeSubnet pulumi.StringInput, loadBalancerType string) pulumi.Output {
	return pulumi.All(clusterName, location, nodeSubnet).
		ApplyT(func(resolved []any) any {
			return Ingress{
				Replicas: ControllerReplicas,
				// The load balancer's name carries the cluster's, so two
				// clusters in one project do not collide on it.
				Name:             resolved[0].(string) + "-ingress",
				Location:         resolved[1].(string),
				LoadBalancerType: loadBalancerType,
				NodeSubnet:       resolved[2].(string),
			}
		})
}

func main() {
	layer.Run(func(r *layer.Runner) error {
		_, err := r.Deploy(Components)

		return err
	})
}
