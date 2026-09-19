package charts

import (
	"strconv"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"
)

// Traefik is the ingress controller, and how anything reaches this cluster
// from outside: the Hetzner load balancer 40-ingress creates forwards to the
// node ports internal/pkg/platform pins, and Traefik answers on them.
// Traefik is the registry key, and what a layer names when it installs this
// chart. Exported because a layer writing the key as a literal is the drift
// this package exists to remove.
const Traefik = "traefik"

// Traefik's keys, as path segments rather than one dotted string, because the
// layer writes them as a nested map and the render check writes them as a
// --set expression. Assembling both from the same pieces is the point.
//
// Traefik accepts a PROXY protocol header only from addresses it is told to
// trust, and the Hetzner load balancer is told to send one. Leave the trusted
// list empty — which a misspelt key does — and Traefik rejects the header on
// every connection that arrives through the load balancer.
const (
	TraefikPorts         = "ports"
	TraefikEntryPointWeb = "web"
	TraefikEntryPointTLS = "websecure"
	TraefikProxyProtocol = "proxyProtocol"
	TraefikTrustedIPs    = "trustedIPs"
	// TraefikNodePort asks Kubernetes for a specific node port instead of
	// letting it allocate one. The Pulumi-managed load balancer forwards to a
	// fixed number, so an unpinned port makes it health-check a closed one.
	TraefikNodePort = "nodePort"
	// TraefikServiceSpec and TraefikServiceType are where this chart puts the
	// Service type — under `service.spec`, not `service.type`, which is the
	// spelling the chart's own values.yaml uses and not the one most charts do.
	TraefikServiceSpec = "spec"
	TraefikServiceType = "type"
	// TraefikService is the top-level key both of those hang from.
	TraefikService = "service"
)

// TraefikProxyProtocolSet is the --set expression for one entry point, built
// from the same constants the layer nests.
func TraefikProxyProtocolSet(entryPoint, cidr string) string {
	return TraefikPorts + "." + entryPoint + "." + TraefikProxyProtocol + "." +
		TraefikTrustedIPs + "[0]=" + cidr
}

// ProxyProtocolProbeCIDR is the range the render check renders with. A
// documentation range rather than this platform's node subnet, which is a
// per-environment value the check has no business knowing: what is verified is
// that the key reaches the rendered arguments at all.
const ProxyProtocolProbeCIDR = "192.0.2.0/24"

func init() {
	register(Definition{
		Key:   Traefik,
		Layer: platform.LayerIngress,
		Chart: Chart{
			Name:       "traefik",
			Repo:       "https://traefik.github.io/charts",
			Version:    "41.6.0", // app v3.7.13
			AppVersion: "v3.7.13",
			Namespace:  Traefik,
		},
		Workloads: []Object{
			{Kind: Deployment, Name: Traefik},
		},
		Probe: traefikProbe,
		Settings: []Setting{
			{
				Set:    []string{PriorityClassName + "=" + PriorityClusterCritical},
				Expect: PriorityLine(PriorityClusterCritical),
				Why: "ingress is how anything reaches this cluster from outside, and with no " +
					"priority the kubelet evicts it beside the workloads it serves",
			},
			{
				Set:    []string{TraefikProxyProtocolSet(TraefikEntryPointWeb, ProxyProtocolProbeCIDR)},
				Expect: "--entryPoints." + TraefikEntryPointWeb + ".proxyProtocol.trustedIPs=" + ProxyProtocolProbeCIDR,
				Why:    "the Hetzner load balancer sends the PROXY header; an entry point that trusts nobody rejects it on every connection",
			},
			{
				Set:    []string{TraefikProxyProtocolSet(TraefikEntryPointTLS, ProxyProtocolProbeCIDR)},
				Expect: "--entryPoints." + TraefikEntryPointTLS + ".proxyProtocol.trustedIPs=" + ProxyProtocolProbeCIDR,
				Why:    "the TLS entry point is behind the same load balancer and needs the same trust, and forgetting it breaks only HTTPS",
			},
			{
				Set: []string{
					TraefikService + "." + TraefikServiceSpec + "." + TraefikServiceType + "=NodePort",
				},
				Expect: "type: NodePort",
				Why: "the chart's default is LoadBalancer, which asks the cloud controller " +
					"manager for a load balancer that Pulumi already manages — both would " +
					"reconcile one object",
			},
			{
				Set: []string{
					TraefikPorts + "." + TraefikEntryPointWeb + "." + TraefikNodePort + "=" +
						strconv.Itoa(platform.IngressNodePortHTTP),
				},
				Expect: "nodePort: " + strconv.Itoa(platform.IngressNodePortHTTP),
				Why: "the Pulumi-managed load balancer forwards to this exact port; unpinned, " +
					"Kubernetes allocates another and every target reports unhealthy against a " +
					"closed one",
			},
		},
	})
}

// TraefikValues is what traefik.yaml.tmpl is executed against.
type TraefikValues struct {
	Replicas int
	// NodeSubnet is the range Traefik trusts a PROXY protocol header from.
	NodeSubnet string
	// NodePortHTTP and NodePortHTTPS are the pinned node ports the
	// Pulumi-managed load balancer forwards to. Both sides read one pair of
	// constants from internal/pkg/platform — see the template for what a
	// mismatch does.
	NodePortHTTP  int
	NodePortHTTPS int
}

// traefikProbe renders the template offline. The ports are the real pinned
// ones, not placeholders: the render check asserts they reach the chart's
// output, which is the only place the pin is observable.
func traefikProbe() any {
	return TraefikValues{
		Replicas:      2,
		NodeSubnet:    ProxyProtocolProbeCIDR,
		NodePortHTTP:  platform.IngressNodePortHTTP,
		NodePortHTTPS: platform.IngressNodePortHTTPS,
	}
}
