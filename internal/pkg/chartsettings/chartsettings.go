// Package chartsettings holds the handful of chart values whose misspelling
// fails silently, together with the effect each one must have on the rendered
// chart.
//
// Why only a handful, when the layers set dozens of values as plain string
// keys: most values fail visibly. A wrong storage size is wrong in the diff; a
// wrong replica count is wrong in `kubectl get`. These do not. Helm accepts an
// unknown key without complaint — even for charts shipping a values.schema.json,
// because the schemas do not forbid extra properties — so a typo leaves the
// chart's default in place and the platform starts, wrong, with nothing said.
//
// Measured, not assumed: rendering Cilium with `kubeProxyReplacment` (one
// letter short) produces `kube-proxy-replacement: "false"` and exits zero.
// That is a cluster whose every ClusterIP blackholes.
//
// The key strings are constants so the layer that sets them and the check that
// verifies them cannot drift apart. That is the whole point — a check holding
// its own copy of the key would pass while the layer misspells it.
package chartsettings

import (
	"strconv"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterref"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"
)

// Cilium keys. The cluster tier disables kube-proxy in the Talos machine
// config, so the replacement is not an optimisation here — without it there is
// no service dataplane at all.
const (
	CiliumKubeProxyReplacement = "kubeProxyReplacement"
	CiliumK8sServiceHost       = "k8sServiceHost"
	CiliumK8sServicePort       = "k8sServicePort"
)

// HcloudCSIDefaultLocation is the key that tells the CSI controller which
// location to create volumes in.
//
// A setting that fails silently, which is why it is here: left empty, the
// chart is still valid and the controller instead discovers its location at
// startup — through the metadata service and an api.hetzner.cloud lookup that
// needs CoreDNS — inside the twenty seconds its liveness probe allows. That
// produced a CrashLoopBackOff on the three-node cluster with nothing logged
// past the driver's start line. The template explains it at length; this is
// the one spelling both it and the render check use.
const HcloudCSIDefaultLocation = "hcloudVolumeDefaultLocation"

// KubePrismPort is the node-local API load balancer Talos enables in the
// cluster tier's machine config, and the port Cilium is pointed at. The two
// are a pair: change one without the other and the CNI cannot reach the API
// server.
//
// The value is clusterref's, not this package's, and the difference is what
// the comment above used to get wrong: it claimed the layer, the machine
// config and the render check all read one value, while the machine config
// held a bare 7445 of its own. Two copies and a comment saying otherwise.
const KubePrismPort = clusterref.KubePrismPort

// Traefik keys, as path segments rather than one dotted string, because the
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

// MetricsServerAddressTypes pins kubelet address resolution to the node's
// internal address. Talos kubelet certificates carry that address, and the
// chart default tries the hostname first — metrics-server then starts and
// every scrape fails, so the autoscaler is silently blind.
const MetricsServerAddressTypes = "--kubelet-preferred-address-types=InternalIP"

// Effect is a setting, the value a layer gives it, and the string that must
// appear in the chart's rendered output as a result.
type Effect struct {
	// Chart is the key into internal/pkg/charts.
	Chart string
	// Release and Namespace match how the layer installs it; some charts put
	// the release name into the rendered content.
	Release   string
	Namespace string

	// Set is the `--set` expressions, built from the constants above. Some
	// effects need more than one: Cilium renders the API port only when the
	// host is set too, so setting the port alone produces nothing at all —
	// itself a silent failure, avoided here only because the layer sets both.
	Set []string

	// Expect must appear in the rendered manifests. It is the EFFECT — the
	// line the chart's own template produces — not the input, so a key the
	// chart ignores cannot satisfy it.
	Expect string

	// Why is printed when the check fails, because the failure is otherwise
	// opaque: a rendered string that is absent says nothing about consequence.
	Why string
}

// HcloudCSIProbeLocation is the location the render check renders with.
// clusterref.ProbeLocation rather than a literal: the value is passed through
// to an env var verbatim, so it has to be a location the schema and the
// topology validator both accept.
const HcloudCSIProbeLocation = clusterref.ProbeLocation

// ProxyProtocolProbeCIDR is the range the render check renders with. It is a
// documentation range rather than this platform's node subnet, which is a
// per-environment value the check has no business knowing: what is being
// verified is that the key reaches the rendered arguments at all.
const ProxyProtocolProbeCIDR = "192.0.2.0/24"

// Effects is what `charts:render-check` verifies.
var Effects = []Effect{
	{
		Chart: "cilium", Release: "cilium", Namespace: "kube-system",
		Set:    []string{CiliumKubeProxyReplacement + "=true"},
		Expect: `kube-proxy-replacement: "true"`,
		Why:    "Talos runs with kube-proxy disabled; without the replacement every ClusterIP blackholes",
	},
	{
		Chart: "cilium", Release: "cilium", Namespace: "kube-system",
		Set: []string{
			CiliumK8sServiceHost + "=localhost",
			CiliumK8sServicePort + "=" + strconv.Itoa(KubePrismPort),
		},
		Expect: `value: "` + strconv.Itoa(KubePrismPort) + `"`,
		Why:    "Cilium reaches the API through KubePrism on the node; a wrong port ties it to one control-plane node's life",
	},
	{
		Chart: "hcloud-csi", Release: "hcloud-csi", Namespace: "kube-system",
		Set:    []string{HcloudCSIDefaultLocation + "=" + HcloudCSIProbeLocation},
		Expect: `value: "` + HcloudCSIProbeLocation + `"`,
		Why: "left empty the controller discovers its location at startup, through the metadata " +
			"service and an api.hetzner.cloud lookup that needs CoreDNS, inside the twenty seconds " +
			"its liveness probe allows — a CrashLoopBackOff with nothing logged past its start line",
	},
	{
		Chart: "traefik", Release: "traefik", Namespace: "traefik",
		Set:    []string{TraefikProxyProtocolSet(TraefikEntryPointWeb, ProxyProtocolProbeCIDR)},
		Expect: "--entryPoints." + TraefikEntryPointWeb + ".proxyProtocol.trustedIPs=" + ProxyProtocolProbeCIDR,
		Why:    "the Hetzner load balancer sends the PROXY header; an entry point that trusts nobody rejects it on every connection",
	},
	{
		Chart: "traefik", Release: "traefik", Namespace: "traefik",
		Set:    []string{TraefikProxyProtocolSet(TraefikEntryPointTLS, ProxyProtocolProbeCIDR)},
		Expect: "--entryPoints." + TraefikEntryPointTLS + ".proxyProtocol.trustedIPs=" + ProxyProtocolProbeCIDR,
		Why:    "the TLS entry point is behind the same load balancer and needs the same trust, and forgetting it breaks only HTTPS",
	},
	{
		Chart: "traefik", Release: "traefik", Namespace: "traefik",
		Set: []string{
			TraefikService + "." + TraefikServiceSpec + "." + TraefikServiceType + "=NodePort",
		},
		Expect: "type: NodePort",
		Why: "the chart's default is LoadBalancer, which asks the cloud controller manager for a " +
			"load balancer that Pulumi already manages — both would reconcile one object",
	},
	{
		Chart: "traefik", Release: "traefik", Namespace: "traefik",
		Set: []string{
			TraefikPorts + "." + TraefikEntryPointWeb + "." + TraefikNodePort + "=" +
				strconv.Itoa(platform.IngressNodePortHTTP),
		},
		Expect: "nodePort: " + strconv.Itoa(platform.IngressNodePortHTTP),
		Why: "the Pulumi-managed load balancer forwards to this exact port; unpinned, Kubernetes " +
			"allocates another and every target reports unhealthy against a closed one",
	},
	{
		Chart: "metrics-server", Release: "metrics-server", Namespace: "kube-system",
		Set:    []string{`args[0]=` + MetricsServerAddressTypes},
		Expect: MetricsServerAddressTypes,
		Why:    "Talos kubelet certificates carry the internal address; the chart default tries the hostname and every scrape fails",
	},
}
