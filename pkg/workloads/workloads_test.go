package workloads_test

import (
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/charts"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/hetzner"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/workloads"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExpected_EveryChartIsPinned(t *testing.T) {
	t.Parallel()

	// A workload naming a chart nobody registered would be checked against a
	// version that does not exist.
	for _, chart := range workloads.Charts() {
		_, err := charts.Get(chart)
		assert.NoError(t, err, "chart %q", chart)
	}
}

func TestExpected_EveryEntryIsComplete(t *testing.T) {
	t.Parallel()

	for _, w := range workloads.Expected {
		label := w.Chart + "/" + w.Name

		assert.NotEmpty(t, w.Release, label)
		assert.NotEmpty(t, w.Namespace, label)
		assert.NotEmpty(t, w.Name, label)
		assert.Contains(t,
			[]workloads.Kind{workloads.Deployment, workloads.StatefulSet, workloads.DaemonSet},
			w.Kind, label)
	}
}

func TestExpected_NamesAreUniquePerNamespace(t *testing.T) {
	t.Parallel()

	// Two entries for the same object would make the render check pass twice
	// and say nothing, and would hide a rename.
	seen := map[string]string{}

	for _, w := range workloads.Expected {
		key := string(w.Kind) + "/" + w.Namespace + "/" + w.Name
		if prev, dup := seen[key]; dup {
			t.Errorf("%s listed twice (charts %q and %q)", key, prev, w.Chart)
		}

		seen[key] = w.Chart
	}
}

func TestExpected_WorkloadNamespaceMatchesItsChart(t *testing.T) {
	t.Parallel()

	// The layer installs each release into the namespace the registry names,
	// so a workload expected somewhere else would never be found — the e2e
	// suite goes looking for an object at an address nothing writes to.
	//
	// One exception is legitimate: a subchart the layer deliberately overrides
	// into a namespace Talos exempts from Pod Security Admission. node-exporter
	// is that case — it needs hostNetwork, hostPID and hostPath volumes, which
	// baseline forbids, so it cannot live beside the rest of the release. Any
	// other namespace is a typo.
	for _, w := range workloads.Expected {
		chart, err := charts.Get(w.Chart)
		require.NoError(t, err)

		if w.Namespace == chart.Namespace {
			continue
		}

		assert.Contains(t, hetzner.PodSecurityExemptNamespaces, w.Namespace,
			"%s is expected in %s, but its chart installs into %s and %s is not a namespace Talos exempts",
			w.Name, w.Namespace, chart.Namespace, w.Namespace)
	}
}

func TestRendered_SkipsOperatorCreatedWorkloads(t *testing.T) {
	t.Parallel()

	// `helm template` cannot show these: the chart emits a custom resource and
	// an operator builds the StatefulSet from it later. Including them would
	// make the offline check fail on a correct chart.
	rendered := workloads.Rendered()

	for _, w := range rendered {
		assert.False(t, w.OperatorCreated, w.Name)
	}

	assert.Less(t, len(rendered), len(workloads.Expected),
		"kube-prometheus-stack contributes at least two operator-created workloads")

	names := map[string]bool{}
	for _, w := range rendered {
		names[w.Name] = true
	}

	assert.False(t, names["prometheus-kube-prometheus-stack-prometheus"])
	assert.False(t, names["alertmanager-kube-prometheus-stack-alertmanager"])
}

func TestForChart(t *testing.T) {
	t.Parallel()

	cilium := workloads.ForChart("cilium")
	require.Len(t, cilium, 2)

	kinds := map[workloads.Kind]string{}
	for _, w := range cilium {
		kinds[w.Kind] = w.Name
	}

	// The agent must be a DaemonSet — a Deployment would run on one node and
	// leave the rest of the cluster with no dataplane.
	assert.Equal(t, "cilium", kinds[workloads.DaemonSet])
	assert.Equal(t, "cilium-operator", kinds[workloads.Deployment])

	assert.Empty(t, workloads.ForChart("nonexistent"))
}

func TestCharts_CoversEveryLayer(t *testing.T) {
	t.Parallel()

	// A layer whose charts are absent here is a layer the offline check and
	// the e2e suite both ignore.
	for _, chart := range []string{
		"hcloud-ccm", "hcloud-csi", "cilium",
		"cert-manager", "external-secrets", "metrics-server",
		"traefik", "argo-cd",
		"kube-prometheus-stack", "loki", "tempo", "alloy",
	} {
		assert.NotEmpty(t, workloads.ForChart(chart), "chart %q has no expected workloads", chart)
	}
}

func TestExpected_EveryChartsWorkloadsAgreeOnTheRelease(t *testing.T) {
	t.Parallel()

	// The render check templates each chart once, with one release name taken
	// from the chart's first entry. Two entries disagreeing would render
	// against one name and compare against another, so the names it looked
	// for could not appear.
	release := map[string]string{}

	for _, w := range workloads.Expected {
		if previous, seen := release[w.Chart]; seen {
			assert.Equal(t, previous, w.Release,
				"chart %q is expected under two release names", w.Chart)

			continue
		}

		release[w.Chart] = w.Release
	}
}
