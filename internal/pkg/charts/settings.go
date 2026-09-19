package charts

// The chart values whose misspelling fails SILENTLY, and the effect each one
// must have on the rendered chart.
//
// Why only a handful, when the layers set dozens of values: most values fail
// visibly. A wrong storage size is wrong in the diff; a wrong replica count is
// wrong in `kubectl get`. These do not. Helm accepts an unknown key without
// complaint — even for charts shipping a values.schema.json, because the
// schemas do not forbid extra properties — so a typo leaves the chart's
// default in place and the platform starts, wrong, with nothing said.
//
// Measured, not assumed: rendering Cilium with `kubeProxyReplacment` (one
// letter short) produces `kube-proxy-replacement: "false"` and exits zero.
// That is a cluster whose every ClusterIP blackholes.
//
// The keys are constants in the chart's own file so that the layer that sets
// one and the check that verifies it cannot drift apart. That is the whole
// point — a check holding its own copy of the key would pass while the layer
// misspells it.

// Setting is one value a layer sets, and the string the chart must render as a
// result. Declared in the chart's own file.
//
// It carries no chart, release or namespace: those are the chart's, declared
// once in its Definition. They used to be repeated on all fourteen rows of one
// table, which is the same repetition that let a workload row disagree with
// the chart above it.
type Setting struct {
	// Set is the `--set` expressions, built from the chart's key constants.
	// Some settings need more than one: Cilium renders the API port only when
	// the host is set too, so setting the port alone produces nothing at all —
	// itself a silent failure, avoided only because the layer sets both.
	Set []string

	// Expect must appear in the rendered manifests. It is the EFFECT — the
	// line the chart's own template produces — not the input, so a key the
	// chart ignores cannot satisfy it.
	Expect string

	// Why is printed when the check fails, because the failure is otherwise
	// opaque: a rendered string that is absent says nothing about consequence.
	Why string
}

// Effect is a Setting with the chart it belongs to, which is what
// `charts:render-check` renders.
type Effect struct {
	Chart     string
	Release   string
	Namespace string

	Set    []string
	Expect string
	Why    string
}

// Effects is every setting to verify, in the order Definitions returns.
func Effects() []Effect {
	var out []Effect

	for _, definition := range Definitions() {
		for _, setting := range definition.Settings {
			out = append(out, Effect{
				Chart:     definition.Key,
				Release:   definition.Release,
				Namespace: definition.Namespace,
				Set:       setting.Set,
				Expect:    setting.Expect,
				Why:       setting.Why,
			})
		}
	}

	return out
}

// Priority classes for the platform's own workloads, and the two names
// Kubernetes ships built in.
//
// What they decide is eviction order, not scheduling luck. Under node memory
// pressure the kubelet ranks pods by QoS class and then by priority, so a
// platform component with no priority is ranked beside the workloads it exists
// to serve — and the ones that hurt are not symmetrical. Losing the CSI node
// plugin means volumes stop mounting on that node; losing the ingress means
// nothing reaches the cluster from outside at all.
//
// Only what the charts do not already do. Measured by rendering each pinned
// chart with its defaults: Cilium sets system-node-critical for the agent and
// system-cluster-critical for its operator, metrics-server sets
// system-cluster-critical, and the Hetzner cloud controller manager defaults
// to system-cluster-critical — that last one is set anyway, because a default
// this repository relies on and does not state is one an upstream release can
// remove quietly.
//
// Argo CD deliberately gets none. It reconciles; it does not serve. A cluster
// whose Argo CD has been evicted keeps running everything it was told to run,
// and marking it cluster-critical would let it outrank the workloads under the
// exact pressure where they matter more.
//
// There is no namespace restriction on either class — Traefik and cert-manager
// are in their own namespaces and use them, which the Kubernetes documentation
// on pod priority permits. ResourceQuota is the mechanism for limiting them,
// and this cluster sets none.
const (
	PriorityClusterCritical = "system-cluster-critical"
	PriorityNodeCritical    = "system-node-critical"

	// PriorityClassName is the key itself. Every chart here spells it the
	// same; what differs is the path to it, which is why each chart's file
	// declares its own segments.
	PriorityClassName = "priorityClassName"
)

// PriorityLine and PriorityLineQuoted are the rendered line to look for, and
// the quoting is the CHART's rather than a choice here: Traefik's template
// emits the value bare while cert-manager, hcloud-csi and the cloud controller
// manager all quote it. Measured by rendering each at its pinned version, after
// asserting the bare form everywhere and watching three of the four fail with
// the value correctly applied — a check that is wrong about the spelling of a
// setting that works.
func PriorityLine(class string) string {
	return PriorityClassName + ": " + class
}

// PriorityLineQuoted is the same line as a chart writes it when it quotes.
func PriorityLineQuoted(class string) string {
	return PriorityClassName + `: "` + class + `"`
}
