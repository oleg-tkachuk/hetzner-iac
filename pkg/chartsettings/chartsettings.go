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

import "strconv"

// Cilium keys. The cluster tier disables kube-proxy in the Talos machine
// config, so the replacement is not an optimisation here — without it there is
// no service dataplane at all.
const (
	CiliumKubeProxyReplacement = "kubeProxyReplacement"
	CiliumK8sServiceHost       = "k8sServiceHost"
	CiliumK8sServicePort       = "k8sServicePort"
)

// KubePrismPort is the node-local API load balancer Talos enables in the
// cluster tier's machine config, and the port Cilium is pointed at. The two
// are a pair: change one without the other and the CNI cannot reach the API
// server. It lives here so the layer, the machine config and the render check
// all read one value.
const KubePrismPort = 7445

// ingress-nginx keys. The PROXY protocol has to be enabled on both the load
// balancer and the controller; one side alone makes every request unparseable.
const (
	IngressUseProxyProtocol    = "use-proxy-protocol"
	IngressUseForwardedHeaders = "use-forwarded-headers"
)

// MetricsServerAddressTypes pins kubelet address resolution to the node's
// internal address. Talos kubelet certificates carry that address, and the
// chart default tries the hostname first — metrics-server then starts and
// every scrape fails, so the autoscaler is silently blind.
const MetricsServerAddressTypes = "--kubelet-preferred-address-types=InternalIP"

// Effect is a setting, the value a layer gives it, and the string that must
// appear in the chart's rendered output as a result.
type Effect struct {
	// Chart is the key into pkg/charts.
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

// Effects is what `task charts:render-check` verifies.
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
		Chart: "ingress-nginx", Release: "ingress-nginx", Namespace: "ingress-nginx",
		Set:    []string{"controller.config." + IngressUseProxyProtocol + "=true"},
		Expect: `use-proxy-protocol: "true"`,
		Why:    "the Hetzner load balancer sends the PROXY header; a controller that does not expect it fails every request",
	},
	{
		Chart: "ingress-nginx", Release: "ingress-nginx", Namespace: "ingress-nginx",
		Set:    []string{"controller.config." + IngressUseForwardedHeaders + "=false"},
		Expect: `use-forwarded-headers: "false"`,
		Why:    "with PROXY protocol carrying the client address, trusting a forwarded header would accept a spoofed one",
	},
	{
		Chart: "metrics-server", Release: "metrics-server", Namespace: "kube-system",
		Set:    []string{`args[0]=` + MetricsServerAddressTypes},
		Expect: MetricsServerAddressTypes,
		Why:    "Talos kubelet certificates carry the internal address; the chart default tries the hostname and every scrape fails",
	},
}
