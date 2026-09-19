package charts

import (
	"fmt"
	"slices"
)

// Kind is a workload kind, spelled as Kubernetes spells it.
//
// Here rather than in internal/pkg/workloads because a chart's file declares
// the objects it produces, and that package now reads them from here. It keeps
// its own names for them, as aliases, so every caller is unaffected.
type Kind string

// The workload kinds this platform installs. Nothing here runs as a Job or a
// CronJob: Helm hook Jobs exist but finish, so they are not something to
// assert is healthy.
const (
	Deployment  Kind = "Deployment"
	StatefulSet Kind = "StatefulSet"
	DaemonSet   Kind = "DaemonSet"
)

// NamespaceKubeSystem is where the charts that are part of the node itself
// install: the CNI, the cloud controller manager, the CSI driver and
// metrics-server. Named because four charts repeat it and because Talos
// exempts exactly this namespace from Pod Security Admission — see
// clusterspec.PodSecurityExemptNamespaces, which is the other half of that
// decision.
const NamespaceKubeSystem = "kube-system"

// Object is one Kubernetes object a chart is expected to produce.
//
// Four fields, and the two that are absent are the point: the chart's release
// name and namespace are properties of the CHART, declared once in its
// Definition, and repeating them per object is how a row comes to disagree
// with the chart above it.
type Object struct {
	Kind Kind
	Name string

	// Namespace overrides the chart's, for a chart that installs into two —
	// a node exporter into kube-system and the rest into its own. Nothing
	// pinned here does that today; the field exists because the alternative
	// is discovering it under a Helm timeout.
	Namespace string

	// OperatorCreated marks an object `helm template` will NOT show because
	// the chart renders a custom resource and an operator turns it into the
	// workload later. The offline render check skips these; the e2e suite
	// does not.
	OperatorCreated bool

	// Optional marks an object the cluster may legitimately not have: a chart
	// installed only under a config key, or one the chart renders only under
	// some values.
	Optional bool
}

// Definition is everything this repository knows about one chart.
//
// One file per chart declares one of these, and the packages that used to hold
// the four halves separately — the pin here, the expected workloads in
// internal/pkg/workloads and the value keys in a package of their own — read
// it instead. The values data stays in internal/pkg/values, which pulls
// Pulumi's SDK and cannot come here: TestChartsPackage_StaysALeaf refuses it.
//
// The reason is not tidiness. Adding KEDA touched sixteen files, and every row
// of the workloads table repeated the chart's layer, release and namespace: a
// property of the chart, written once per object, which is exactly the shape
// that produced `// Layer 20 — CNI` on a chart layer 10 installs.
type Definition struct {
	// Key is the registry key, and the name of the file this is declared in.
	Key string

	// Chart is the pin: what to install, from where, at which version.
	Chart

	// Release is the Helm release name, which most charts prefix object names
	// with. Empty means the key, which is true of every chart but the cloud
	// controller manager.
	Release string

	// Layer is the layer that installs it, from internal/pkg/platform's
	// constants. One per chart rather than one per object, because no chart
	// here is installed by two layers — and a chart that were would be a
	// question about the layer split rather than a field.
	Layer string

	// Workloads are the objects it is expected to produce.
	Workloads []Object

	// Settings are the values whose misspelling fails silently, each with the
	// line the chart must render as a result. See Setting.
	Settings []Setting
}

// definitions is built by the per-chart files, not written here: a list in this
// file would be the second place a chart is named, which is what one file per
// chart exists to remove.
var definitions = map[string]Definition{}

// register adds a chart. Called from each chart's own file.
//
// It panics on a duplicate or a missing key, which is a programming error that
// every test run finds: the alternative is a chart silently replacing another
// and a registry that is shorter than the directory.
func register(definition Definition) {
	if definition.Key == "" {
		panic("chart definition with no key")
	}

	if _, duplicate := definitions[definition.Key]; duplicate {
		panic(fmt.Sprintf("chart %q is declared twice", definition.Key))
	}

	if definition.Release == "" {
		definition.Release = definition.Key
	}

	definitions[definition.Key] = definition
}

// Lookup returns a whole definition.
func Lookup(key string) (Definition, error) {
	definition, known := definitions[key]
	if !known {
		return Definition{}, fmt.Errorf("unknown chart %q; known charts: %v", key, Keys())
	}

	return definition, nil
}

// Definitions returns every definition, ordered by layer and then by key.
//
// Ordered, because callers build tables from it and a map's order would make
// their output differ between runs. Layer first, and the layer names sort into
// dependency order by construction — `10-node-platform` before
// `30-cluster-services` — so a derived list reads in the order the platform
// comes up.
func Definitions() []Definition {
	out := make([]Definition, 0, len(definitions))
	for _, definition := range definitions {
		out = append(out, definition)
	}

	slices.SortFunc(out, func(a, b Definition) int {
		if a.Layer != b.Layer {
			return cmpString(a.Layer, b.Layer)
		}

		return cmpString(a.Key, b.Key)
	})

	return out
}

// cmpString is strings.Compare without the import, kept here so this package
// stays as small as it is: everything imports it, including the checks.
func cmpString(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
