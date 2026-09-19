// Package workloads names the Kubernetes objects each layer's charts create.
//
// One table, read by two things: the e2e suite asserts these are healthy on a
// real cluster, and `charts:render-check` proves the charts still produce
// them by rendering offline. Keeping the names in one place is what makes the
// offline check meaningful — a chart upgrade that renames a Deployment fails
// in seconds instead of at 3am against production.
package workloads

import (
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/charts"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"
)

// The layers that install charts. Aliases for internal/pkg/platform's
// constants, which is where they live now: internal/pkg/charts declares which
// layer installs each chart, and this package imports charts — so the
// constants cannot live downstream of it.
const (
	LayerNodePlatform    = platform.LayerNodePlatform
	LayerClusterServices = platform.LayerClusterServices
	LayerIngress         = platform.LayerIngress
	LayerGitOps          = platform.LayerGitOps
)

// Kind and the kinds themselves are internal/pkg/charts', because a chart's
// own file declares the objects it produces. Aliased here so every caller —
// the e2e suite, the render check, the gates — is unaffected.
type Kind = charts.Kind

// The kinds, by the names this package's callers already use.
const (
	Deployment  = charts.Deployment
	StatefulSet = charts.StatefulSet
	DaemonSet   = charts.DaemonSet
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
// Derived from internal/pkg/charts rather than written here, and the table it
// replaces is the argument for the move: every row repeated the chart's layer,
// release and namespace — four properties of the CHART, written once per
// object — and a row that disagreed with the chart above it was a comment away
// from being believed. `cilium` was labelled `// Layer 20 — CNI` for months
// while 10-node-platform installed it.
//
// Still a package-level var and still a []Workload, because the gates, the
// render check and the e2e suite range over it.
//
// Verified against the pinned chart versions by rendering them; see
// `charts:render-check`.
var Expected = expected()

// expected flattens the declarations into rows.
//
// The namespace and the release come from the chart unless an object overrides
// the namespace, which nothing pinned here does — the field exists because a
// chart installing into two namespaces is a thing that happens, and finding
// out under a Helm timeout is the expensive way.
func expected() []Workload {
	var out []Workload

	for _, definition := range charts.Definitions() {
		for _, object := range definition.Workloads {
			namespace := object.Namespace
			if namespace == "" {
				namespace = definition.Namespace
			}

			out = append(out, Workload{
				Chart:           definition.Key,
				Layer:           definition.Layer,
				Release:         definition.Release,
				Namespace:       namespace,
				Kind:            object.Kind,
				Name:            object.Name,
				OperatorCreated: object.OperatorCreated,
				Optional:        object.Optional,
			})
		}
	}

	return out
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
