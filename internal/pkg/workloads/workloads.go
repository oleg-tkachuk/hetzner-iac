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

// The layers that install charts, spelled as their directories under layers/
// are. Held equal to those directories by TestWorkloadLayers_AreRealLayers,
// and to what each layer's own component set installs by layertest.Check.
//
// Named rather than written out at each entry because the same string has to
// match a directory name and a layer's own idea of itself, and a comment
// saying which layer a workload belongs to matches nothing: `cilium` carried
// `// Layer 20 — CNI` for months while 10-node-platform installed it, and
// 20-network-policy installs no chart at all.
const (
	LayerNodePlatform    = "10-node-platform"
	LayerClusterServices = "30-cluster-services"
	LayerIngress         = "40-ingress"
	LayerGitOps          = "50-gitops"
)

// Workload is one object a chart is expected to produce.
type Workload struct {
	// Chart is the key into internal/pkg/charts.
	Chart string
	// Layer is the layer that installs the chart, one of the constants above.
	// It is what makes "which layer is this from" a fact a test can check
	// rather than a comment that goes stale.
	Layer string
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
	// Cloud integration: the cloud controller manager clears Talos's
	// uninitialized taint, and the CSI node plugin attaches volumes.
	{Layer: LayerNodePlatform, Chart: "hcloud-ccm", Release: "hcloud-cloud-controller-manager", Namespace: "kube-system", Kind: Deployment, Name: "hcloud-cloud-controller-manager"},
	{Layer: LayerNodePlatform, Chart: "hcloud-csi", Release: "hcloud-csi", Namespace: "kube-system", Kind: DaemonSet, Name: "hcloud-csi-node"},

	// The CNI, installed by the same layer: a node without it stays NotReady,
	// so nothing above can be scheduled.
	{Layer: LayerNodePlatform, Chart: "cilium", Release: "cilium", Namespace: "kube-system", Kind: DaemonSet, Name: "cilium"},
	{Layer: LayerNodePlatform, Chart: "cilium", Release: "cilium", Namespace: "kube-system", Kind: Deployment, Name: "cilium-operator"},

	// Core cluster services.
	{Layer: LayerClusterServices, Chart: "cert-manager", Release: "cert-manager", Namespace: "cert-manager", Kind: Deployment, Name: "cert-manager"},
	{Layer: LayerClusterServices, Chart: "cert-manager", Release: "cert-manager", Namespace: "cert-manager", Kind: Deployment, Name: "cert-manager-webhook"},
	{Layer: LayerClusterServices, Chart: "cert-manager", Release: "cert-manager", Namespace: "cert-manager", Kind: Deployment, Name: "cert-manager-cainjector"},
	{Layer: LayerClusterServices, Chart: "external-secrets", Release: "external-secrets", Namespace: "external-secrets", Kind: Deployment, Name: "external-secrets"},
	{Layer: LayerClusterServices, Chart: "metrics-server", Release: "metrics-server", Namespace: "kube-system", Kind: Deployment, Name: "metrics-server"},

	// Event-driven autoscaling, installed only when `kedaEnabled` is set.
	{Layer: LayerClusterServices, Chart: "keda", Release: "keda", Namespace: "keda", Kind: Deployment, Name: "keda-operator", Optional: true},
	{Layer: LayerClusterServices, Chart: "keda", Release: "keda", Namespace: "keda", Kind: Deployment, Name: "keda-operator-metrics-apiserver", Optional: true},
	{Layer: LayerClusterServices, Chart: "keda", Release: "keda", Namespace: "keda", Kind: Deployment, Name: "keda-admission-webhooks", Optional: true},

	// Ingress.
	{Layer: LayerIngress, Chart: "traefik", Release: "traefik", Namespace: "traefik", Kind: Deployment, Name: "traefik"},

	// GitOps.
	{Layer: LayerGitOps, Chart: "argo-cd", Release: "argo-cd", Namespace: "argocd", Kind: Deployment, Name: "argo-cd-argocd-server"},
	{Layer: LayerGitOps, Chart: "argo-cd", Release: "argo-cd", Namespace: "argocd", Kind: Deployment, Name: "argo-cd-argocd-repo-server"},
	{Layer: LayerGitOps, Chart: "argo-cd", Release: "argo-cd", Namespace: "argocd", Kind: StatefulSet, Name: "argo-cd-argocd-application-controller"},
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

// LayerOf returns the layer that installs a chart, and false when the table
// does not name it.
func LayerOf(chart string) (string, bool) {
	for _, w := range Expected {
		if w.Chart == chart {
			return w.Layer, true
		}
	}

	return "", false
}

// Layers lists every layer the table names, in first-seen order. A layer that
// installs no chart — 20-network-policy writes only policies — is absent, and
// that absence is correct rather than a gap.
func Layers() []string {
	seen := map[string]struct{}{}

	var out []string

	for _, w := range Expected {
		if _, dup := seen[w.Layer]; dup {
			continue
		}

		seen[w.Layer] = struct{}{}
		out = append(out, w.Layer)
	}

	return out
}
