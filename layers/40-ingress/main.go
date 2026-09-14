// Command ingress installs Traefik behind a Hetzner load balancer.
//
// The load balancer is created HERE, through the Hetzner provider, rather than
// by asking the cloud controller manager for one. Both were tried against a
// live cluster; pkg/hetzner.NewIngressLoadBalancer records what the CCM route
// cost. In short: a CCM-managed load balancer is invisible to `plan` and
// `destroy`, and it refuses to target a control-plane node at all.
//
// The values themselves are pkg/values/traefik.yaml.tmpl. What is left here is
// the part Go has to do: read stack config, resolve what the cluster tier
// published, and create the Hetzner resources the chart no longer asks for.
package main

import (
	"fmt"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/hetzner"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/layer"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/platform"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/values"

	"github.com/pulumi/pulumi-hcloud/sdk/go/hcloud"
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

// OutputAddress is the stack output carrying the ingress address.
//
// Exported because it is the one fact an operator needs from this layer and
// there is now somewhere to read it from: under the cloud controller manager
// the address existed only on a Service's status, so `pulumi stack output` had
// nothing to say about how the cluster is reached.
const OutputAddress = "ingressIp"

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
// Asset renders inside an apply, so a template error reaches the engine as a
// failed input rather than being returned here — hence the nil.
func renderValues(r *layer.Runner) (pulumi.AssetOrArchiveArrayInput, error) {
	return values.Asset(Chart, IngressData(r.Cluster.NodeSubnet)), nil
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
				// The same two constants pkg/hetzner points the load
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
	hcloudProvider, err := hcloud.NewProvider(r.Ctx, "hcloud", &hcloud.ProviderArgs{
		Token: r.Cluster.HcloudToken,
	})
	if err != nil {
		return fmt.Errorf("hcloud provider: %w", err)
	}

	address, err := hetzner.NewIngressLoadBalancer(r.Ctx, "ingress", hetzner.IngressLoadBalancerArgs{
		ClusterName:      r.Cluster.ClusterName,
		Location:         r.Cluster.Location,
		NetworkID:        r.Cluster.NetworkID,
		LoadBalancerType: r.StringOr("loadBalancerType", DefaultLoadBalancerType),
	}, pulumi.Provider(hcloudProvider))
	if err != nil {
		return err
	}

	r.Ctx.Export(OutputAddress, address)

	return nil
}

func main() {
	layer.Run(deploy)
}
