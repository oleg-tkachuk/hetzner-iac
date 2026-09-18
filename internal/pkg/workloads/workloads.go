// Package workloads names the Kubernetes objects each layer's charts create.
//
// One table, read by two things: the e2e suite asserts these are healthy on a
// real cluster, and `charts:render-check` proves the charts still produce
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
	// Chart is the key into internal/pkg/charts.
	Chart string
	// Release is the Helm release name, which most charts prefix names with.
	Release   string
	Namespace string
	Kind      Kind
	Name      string

	// OperatorCreated marks a workload that `helm template` will NOT show
	// because the chart renders a custom resource and an operator turns it
	// into the workload later — the chart emits the CR, and a controller
	// creates the Deployment or StatefulSet from it afterwards.
	//
	// Nothing pinned here does that today; the field stays because the render
	// check has to know the difference, and finding out the hard way costs a
	// Helm timeout. The offline check skips these; the e2e suite does not.
	OperatorCreated bool

	// Optional marks a workload the cluster may legitimately not have.
	//
	// Two ways that happens: a chart renders it only under some values, or a
	// layer declines to install the chart at all — KEDA is installed only when
	// `kedaEnabled` is set. The render check tolerates an absent one; the e2e
	// suite skips it rather than waiting out its readiness timeout on
	// something that was never created.
	//
	// It does not weaken either check where the workload IS there. KEDA's
	// three are rendered unconditionally by their chart, so `render-check`
	// still proves the names, and e2e still asserts availability on a cluster
	// that has them.
	Optional bool
}

// Expected is every workload the platform layers install.
//
// Verified against the pinned chart versions by rendering them; see
// `charts:render-check`.
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

	// Layer 30 — event-driven autoscaling, installed only when `kedaEnabled`
	// is set.
	{Chart: "keda", Release: "keda", Namespace: "keda", Kind: Deployment, Name: "keda-operator", Optional: true},
	{Chart: "keda", Release: "keda", Namespace: "keda", Kind: Deployment, Name: "keda-operator-metrics-apiserver", Optional: true},
	{Chart: "keda", Release: "keda", Namespace: "keda", Kind: Deployment, Name: "keda-admission-webhooks", Optional: true},

	// Layer 40 — ingress.
	{Chart: "traefik", Release: "traefik", Namespace: "traefik", Kind: Deployment, Name: "traefik"},

	// Layer 50 — GitOps.
	{Chart: "argo-cd", Release: "argo-cd", Namespace: "argocd", Kind: Deployment, Name: "argo-cd-argocd-server"},
	{Chart: "argo-cd", Release: "argo-cd", Namespace: "argocd", Kind: Deployment, Name: "argo-cd-argocd-repo-server"},
	{Chart: "argo-cd", Release: "argo-cd", Namespace: "argocd", Kind: StatefulSet, Name: "argo-cd-argocd-application-controller"},
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
