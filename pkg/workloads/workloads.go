// Package workloads names the Kubernetes objects each layer's charts create.
//
// One table, read by two things: the e2e suite asserts these are healthy on a
// real cluster, and `task charts:render-check` proves the charts still produce
// them by rendering offline. Keeping the names in one place is what makes the
// offline check meaningful — a chart upgrade that renames a Deployment fails
// in seconds instead of at 3am against production.
package workloads

// Kind is the workload kind, spelled as Kubernetes spells it.
type Kind string

// The workload kinds this platform installs. Nothing here runs as a Job or a
// CronJob: Helm hook Jobs exist but finish, so they are not something to
// assert is healthy.
const (
	Deployment  Kind = "Deployment"
	StatefulSet Kind = "StatefulSet"
	DaemonSet   Kind = "DaemonSet"
)

// Workload is one object a chart is expected to produce.
type Workload struct {
	// Chart is the key into pkg/charts.
	Chart string
	// Release is the Helm release name, which most charts prefix names with.
	Release   string
	Namespace string
	Kind      Kind
	Name      string

	// OperatorCreated marks a workload that `helm template` will NOT show
	// because the chart renders a custom resource and an operator turns it
	// into the workload later. kube-prometheus-stack does this for Prometheus
	// and Alertmanager: the chart emits a Prometheus CR named
	// kube-prometheus-stack-prometheus, and the operator creates the
	// StatefulSet prometheus-kube-prometheus-stack-prometheus from it.
	//
	// The offline render check skips these; the e2e suite does not.
	OperatorCreated bool

	// Optional marks a workload that exists only under some values. It must
	// not fail the render check when absent.
	Optional bool
}

// Expected is every workload the platform layers install.
//
// Verified against the pinned chart versions by rendering them; see
// `task charts:render-check`.
var Expected = []Workload{
	// Layer 10 — cloud integration.
	{Chart: "hcloud-ccm", Release: "hcloud-cloud-controller-manager", Namespace: "kube-system", Kind: Deployment, Name: "hcloud-cloud-controller-manager"},
	{Chart: "hcloud-csi", Release: "hcloud-csi", Namespace: "kube-system", Kind: DaemonSet, Name: "hcloud-csi-node"},

	// Layer 20 — CNI.
	{Chart: "cilium", Release: "cilium", Namespace: "kube-system", Kind: DaemonSet, Name: "cilium"},
	{Chart: "cilium", Release: "cilium", Namespace: "kube-system", Kind: Deployment, Name: "cilium-operator"},

	// Layer 30 — core.
	{Chart: "cert-manager", Release: "cert-manager", Namespace: "cert-manager", Kind: Deployment, Name: "cert-manager"},
	{Chart: "cert-manager", Release: "cert-manager", Namespace: "cert-manager", Kind: Deployment, Name: "cert-manager-webhook"},
	{Chart: "cert-manager", Release: "cert-manager", Namespace: "cert-manager", Kind: Deployment, Name: "cert-manager-cainjector"},
	{Chart: "external-secrets", Release: "external-secrets", Namespace: "external-secrets", Kind: Deployment, Name: "external-secrets"},
	{Chart: "metrics-server", Release: "metrics-server", Namespace: "kube-system", Kind: Deployment, Name: "metrics-server"},

	// Layer 40 — ingress.
	{Chart: "ingress-nginx", Release: "ingress-nginx", Namespace: "ingress-nginx", Kind: Deployment, Name: "ingress-nginx-controller"},

	// Layer 50 — GitOps.
	{Chart: "argo-cd", Release: "argo-cd", Namespace: "argocd", Kind: Deployment, Name: "argo-cd-argocd-server"},
	{Chart: "argo-cd", Release: "argo-cd", Namespace: "argocd", Kind: Deployment, Name: "argo-cd-argocd-repo-server"},
	{Chart: "argo-cd", Release: "argo-cd", Namespace: "argocd", Kind: StatefulSet, Name: "argo-cd-argocd-application-controller"},

	// Layer 60 — observability.
	{Chart: "kube-prometheus-stack", Release: "kube-prometheus-stack", Namespace: "observability", Kind: Deployment, Name: "kube-prometheus-stack-grafana"},
	{Chart: "kube-prometheus-stack", Release: "kube-prometheus-stack", Namespace: "observability", Kind: Deployment, Name: "kube-prometheus-stack-operator"},
	{Chart: "kube-prometheus-stack", Release: "kube-prometheus-stack", Namespace: "observability", Kind: StatefulSet, Name: "prometheus-kube-prometheus-stack-prometheus", OperatorCreated: true},
	{Chart: "kube-prometheus-stack", Release: "kube-prometheus-stack", Namespace: "observability", Kind: StatefulSet, Name: "alertmanager-kube-prometheus-stack-alertmanager", OperatorCreated: true},
	{Chart: "loki", Release: "loki", Namespace: "observability", Kind: StatefulSet, Name: "loki"},
	{Chart: "loki", Release: "loki", Namespace: "observability", Kind: Deployment, Name: "loki-gateway"},
	{Chart: "tempo", Release: "tempo", Namespace: "observability", Kind: StatefulSet, Name: "tempo"},
	{Chart: "alloy", Release: "alloy", Namespace: "observability", Kind: DaemonSet, Name: "alloy"},
}

// Rendered returns the workloads `helm template` should show — everything an
// operator does not create later.
func Rendered() []Workload {
	out := make([]Workload, 0, len(Expected))

	for _, w := range Expected {
		if !w.OperatorCreated {
			out = append(out, w)
		}
	}

	return out
}

// ForChart returns every expected workload of one chart.
func ForChart(chart string) []Workload {
	var out []Workload

	for _, w := range Expected {
		if w.Chart == chart {
			out = append(out, w)
		}
	}

	return out
}

// Charts lists every chart named in the table, in first-seen order.
func Charts() []string {
	seen := map[string]struct{}{}

	var out []string

	for _, w := range Expected {
		if _, dup := seen[w.Chart]; dup {
			continue
		}

		seen[w.Chart] = struct{}{}

		out = append(out, w.Chart)
	}

	return out
}
