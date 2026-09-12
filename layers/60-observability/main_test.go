package main

import (
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/hetzner"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/layer/layertest"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/observability"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/platform"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/values"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// render is the values YAML a chart's template produces, parsed.
//
// Through the template rather than around it: the rendered file is what
// reaches Helm, so a test reading a Go struct would check something the chart
// never sees.
func render(t *testing.T, chart string, data any) map[string]any {
	t.Helper()

	text, err := values.Render(chart, data)
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(text), &out), "%s must render valid yaml", chart)

	return out
}

func nested(t *testing.T, in map[string]any, keys ...string) map[string]any {
	t.Helper()

	for _, key := range keys {
		next, ok := in[key].(map[string]any)
		require.True(t, ok, "no map at %q", key)

		in = next
	}

	return in
}

func prometheus(t *testing.T) map[string]any {
	t.Helper()

	return render(t, PrometheusChart, PrometheusData(DefaultRetention, DefaultMetricsSize))
}

func loki(t *testing.T) map[string]any {
	t.Helper()

	return render(t, "loki", LokiData())
}

func tempo(t *testing.T) map[string]any {
	t.Helper()

	return render(t, "tempo", TempoData())
}

func alloy(t *testing.T) map[string]any {
	t.Helper()

	return render(t, "alloy", AlloyData())
}

// claimSize is the storage a volume claim template asks for.
func claimSize(t *testing.T, claim map[string]any) string {
	t.Helper()

	size, ok := nested(t, claim, "resources", "requests")["storage"].(string)
	require.True(t, ok)

	return size
}

func TestPrometheusValues_DisablesTheTargetsTalosDoesNotExpose(t *testing.T) {
	t.Parallel()

	// etcd, the scheduler and the controller manager listen on localhost only
	// under Talos, and kube-proxy does not run because Cilium replaced it.
	// Leaving these enabled produces permanently failing scrape targets and
	// alerts that fire for ever — which is how an alerting stack gets ignored.
	rendered := prometheus(t)

	for _, target := range []string{"kubeEtcd", "kubeScheduler", "kubeControllerManager", "kubeProxy"} {
		assert.Equal(t, false, nested(t, rendered, target)["enabled"], target)
	}

	// The node exporter does work under Talos and is the main source of node
	// metrics, so it must stay on.
	assert.Equal(t, true, nested(t, rendered, "nodeExporter")["enabled"])
}

func TestPrometheusValues_DiscoversMonitorsFromEveryNamespace(t *testing.T) {
	t.Parallel()

	// Without this, Prometheus only picks up monitors labelled with its own
	// release, so nothing another layer creates is ever scraped.
	spec := nested(t, prometheus(t), "prometheus", "prometheusSpec")

	assert.Equal(t, false, spec["serviceMonitorSelectorNilUsesHelmValues"])
	assert.Equal(t, false, spec["podMonitorSelectorNilUsesHelmValues"])
	assert.Equal(t, false, spec["ruleSelectorNilUsesHelmValues"])
}

func TestPrometheusValues_PersistsMetrics(t *testing.T) {
	t.Parallel()

	// The chart default is an emptyDir, which loses every series when the pod
	// moves — and a pod moving is the normal case.
	claim := nested(t, prometheus(t),
		"prometheus", "prometheusSpec", "storageSpec", "volumeClaimTemplate", "spec")

	assert.Equal(t, platform.StorageClass, claim["storageClassName"])
	assert.Equal(t, DefaultMetricsSize, claimSize(t, claim))
}

func TestPrometheusValues_RetentionAndSizeArePassedThrough(t *testing.T) {
	t.Parallel()

	// The two knobs a stack is expected to tune.
	rendered := render(t, PrometheusChart, PrometheusData("90d", "200Gi"))
	spec := nested(t, rendered, "prometheus", "prometheusSpec")

	assert.Equal(t, "90d", spec["retention"])
	assert.Equal(t, "200Gi",
		claimSize(t, nested(t, spec, "storageSpec", "volumeClaimTemplate", "spec")))
}

func TestEveryPersistentComponentUsesTheCSIStorageClass(t *testing.T) {
	t.Parallel()

	// A component that silently falls back to the default storage class ends
	// up on a volume nobody provisioned, and stays Pending.
	rendered := prometheus(t)

	assert.Equal(t, platform.StorageClass,
		nested(t, rendered, "grafana", "persistence")["storageClassName"])

	assert.Equal(t, platform.StorageClass,
		nested(t, rendered, "alertmanager", "alertmanagerSpec", "storage",
			"volumeClaimTemplate", "spec")["storageClassName"])

	// Loki spells the key differently from everything else — storageClass,
	// not storageClassName — which is exactly the sort of thing to pin.
	assert.Equal(t, platform.StorageClass,
		nested(t, loki(t), "singleBinary", "persistence")["storageClass"])

	assert.Equal(t, platform.StorageClass,
		nested(t, tempo(t), "persistence")["storageClassName"])
}

func TestLokiValues_RunsOneTopologyNotTwo(t *testing.T) {
	t.Parallel()

	// SingleBinary mode plus non-zero read/write/backend replicas deploys
	// both topologies at once — which starts, and then behaves strangely.
	rendered := loki(t)

	assert.Equal(t, "SingleBinary", rendered["deploymentMode"])

	for _, component := range []string{"read", "write", "backend"} {
		assert.Equal(t, float64(0), nested(t, rendered, component)["replicas"], component)
	}
}

func TestLokiValues_StaysOnTheFilesystem(t *testing.T) {
	t.Parallel()

	section := nested(t, loki(t), "loki")
	storage := nested(t, section, "storage")

	assert.Equal(t, "filesystem", storage["type"])
	assert.NotContains(t, storage, "bucketNames")

	// No retention: the volume bounds itself by filling up, and
	// retention_enabled without a bucket would delete logs for no reason.
	assert.NotContains(t, section, "compactor")
	assert.NotContains(t, section, "limits_config")
}

func TestLokiValues_SchemaAgreesWithStorage(t *testing.T) {
	t.Parallel()

	// The schema decides where Loki reads chunks, storage decides where it
	// writes them. Disagreeing produces a Loki that ingests happily and
	// returns nothing.
	section := nested(t, loki(t), "loki")

	configs, ok := nested(t, section, "schemaConfig")["configs"].([]any)
	require.True(t, ok)
	require.Len(t, configs, 1)

	entry, ok := configs[0].(map[string]any)
	require.True(t, ok)

	assert.Equal(t, "filesystem", entry["object_store"])
	assert.Equal(t, nested(t, section, "storage")["type"], entry["object_store"])
}

func TestTempoValues_LeavesStorageToTheChart(t *testing.T) {
	t.Parallel()

	// The chart's defaults are already the local backend on the volume, so
	// this sets no storage key at all. Restating a default is a values diff
	// that renders identically — noise in a review, and one more line to keep
	// in step with the chart.
	section := nested(t, tempo(t), "tempo")

	assert.NotContains(t, section, "storage")
	assert.Equal(t, TempoRetention, section["retention"])
}

func TestAlloyValues_ShipsLogsToTheLokiGateway(t *testing.T) {
	t.Parallel()

	// The write endpoint has to match the Service the Loki chart creates in
	// SingleBinary mode. A wrong host here means logs are collected and
	// silently dropped.
	//
	// The config is a multi-line string indented into a values key, so this
	// also asserts the template's indentation survived: a misindented block
	// would make the whole file invalid, which render would have caught.
	content, ok := nested(t, alloy(t), "alloy", "configMap")["content"].(string)
	require.True(t, ok)

	assert.Contains(t, content, observability.LokiGateway)
	assert.Contains(t, content, "/loki/api/v1/push")
}

func TestAlloyValues_RunsOnEveryNode(t *testing.T) {
	t.Parallel()

	// Log collection is per-node work; a Deployment would collect from
	// whichever node it happened to land on.
	assert.Equal(t, "daemonset", nested(t, alloy(t), "controller")["type"])
}

func TestAlloyValues_AskForNoHostMounts(t *testing.T) {
	t.Parallel()

	// The chart's mounts — varlog and dockercontainers — are the only things
	// in it that render a hostPath, and a hostPath is what Pod Security
	// baseline refuses. Talos enforces baseline in every namespace but
	// kube-system, and a DaemonSet that violates it gets no pods at all:
	// DESIRED 1, CURRENT 0, and Helm waiting out its whole timeout.
	//
	// Nothing needs them: the collector reads logs through the Kubernetes API.
	// TestAlloyConfig_CollectsThroughTheAPI pins the other half of that.
	assert.NotContains(t, nested(t, alloy(t), "alloy"), "mounts",
		"a host mount puts this DaemonSet outside Pod Security baseline, and nothing reads one")
}

func TestGrafanaValues_RegisterLokiAndTempoAtThePinnedEndpoints(t *testing.T) {
	t.Parallel()

	// Registered here rather than by their own charts, so three charts do not
	// race to write one datasource list.
	sources, ok := nested(t, prometheus(t), "grafana")["additionalDataSources"].([]any)
	require.True(t, ok)
	require.Len(t, sources, 2)

	urls := map[string]string{}

	for _, source := range sources {
		fields, ok := source.(map[string]any)
		require.True(t, ok)

		sourceType, ok := fields["type"].(string)
		require.True(t, ok)

		url, ok := fields["url"].(string)
		require.True(t, ok)

		urls[sourceType] = url
	}

	assert.Equal(t, observability.LokiGateway, urls["loki"])
	assert.Equal(t, observability.TempoHTTP, urls["tempo"])
}

func TestPrometheusValues_PutNodeExporterWhereItCanRun(t *testing.T) {
	t.Parallel()

	// Talos enables Pod Security Admission with `enforce: baseline` for every
	// namespace except kube-system, and node-exporter needs hostNetwork,
	// hostPID and hostPath volumes. In observability its pods are not created
	// at all — the DaemonSet reports DESIRED 1, CURRENT 0 — and Helm waits out
	// its whole timeout with every other workload in the release Ready. That
	// cost two failed deploys before one event on the DaemonSet explained it.
	exporter := nested(t, prometheus(t), "prometheus-node-exporter")

	assert.Equal(t, NodeExporterNamespace, exporter["namespaceOverride"],
		"the subchart must be overridden, or it lands in the release namespace")
	assert.Contains(t, hetzner.PodSecurityExemptNamespaces, NodeExporterNamespace,
		"node-exporter must install where Talos exempts Pod Security Admission")
}

func TestComponents(t *testing.T) {
	t.Parallel()

	layertest.Check(t, Components)
}

func TestComponents_EverythingFollowsPrometheus(t *testing.T) {
	t.Parallel()

	// Loki, Tempo and Alloy each register a datasource or a ServiceMonitor
	// against the Prometheus operator, so none of them can be created before
	// the chart that installs it. This used to be a pulumi.DependsOn built by
	// hand and passed to three calls; a fourth component added without it
	// would race, and the symptom is a missing datasource rather than an error.
	for _, component := range Components {
		if component.Chart == PrometheusChart {
			assert.Empty(t, component.After, "%s must not wait for anything", component.Chart)

			continue
		}

		assert.Contains(t, component.After, PrometheusChart,
			"%s registers against the Prometheus operator and must follow it", component.Chart)
	}
}
