package main

import (
	"os"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/hetzner"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/layer/layertest"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/observability"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/platform"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrometheusValues_DisablesTheTargetsTalosDoesNotExpose(t *testing.T) {
	t.Parallel()

	// etcd, the scheduler and the controller manager listen on localhost only
	// under Talos, and kube-proxy does not run because Cilium replaced it.
	// Leaving these enabled produces permanently failing scrape targets and
	// alerts that fire forever — which is how an alerting stack gets ignored.
	values := PrometheusValues(DefaultRetention, DefaultMetricsSize)

	for _, target := range []string{"kubeEtcd", "kubeScheduler", "kubeControllerManager", "kubeProxy"} {
		section, ok := values[target].(pulumi.Map)
		require.True(t, ok, target)
		assert.Equal(t, pulumi.Bool(false), section["enabled"], target)
	}

	// The node exporter does work under Talos and is the main source of node
	// metrics, so it must stay on.
	nodeExporter, ok := values["nodeExporter"].(pulumi.Map)
	require.True(t, ok)
	assert.Equal(t, pulumi.Bool(true), nodeExporter["enabled"])
}

func TestPrometheusValues_DiscoversMonitorsFromEveryNamespace(t *testing.T) {
	t.Parallel()

	// Without this, Prometheus only picks up monitors labelled with its own
	// release, so nothing another layer creates is ever scraped.
	spec := prometheusSpec(t)

	assert.Equal(t, pulumi.Bool(false), spec["serviceMonitorSelectorNilUsesHelmValues"])
	assert.Equal(t, pulumi.Bool(false), spec["podMonitorSelectorNilUsesHelmValues"])
	assert.Equal(t, pulumi.Bool(false), spec["ruleSelectorNilUsesHelmValues"])
}

func TestPrometheusValues_PersistsMetrics(t *testing.T) {
	t.Parallel()

	// The chart default is an emptyDir, which loses every series when the pod
	// moves — and a pod moving is the normal case.
	spec := prometheusSpec(t)

	storage, ok := spec["storageSpec"].(pulumi.Map)
	require.True(t, ok)

	template, ok := storage["volumeClaimTemplate"].(pulumi.Map)
	require.True(t, ok)

	claim, ok := template["spec"].(pulumi.Map)
	require.True(t, ok)

	assert.Equal(t, pulumi.String(platform.StorageClass), claim["storageClassName"])
	assert.Equal(t, pulumi.String(DefaultMetricsSize), resourceRequest(t, claim))
}

func TestPrometheusValues_RetentionAndSizeArePassedThrough(t *testing.T) {
	t.Parallel()

	values := PrometheusValues("90d", "200Gi")

	prometheus, ok := values["prometheus"].(pulumi.Map)
	require.True(t, ok)

	spec, ok := prometheus["prometheusSpec"].(pulumi.Map)
	require.True(t, ok)

	assert.Equal(t, pulumi.String("90d"), spec["retention"])

	storage, _ := spec["storageSpec"].(pulumi.Map)
	template, _ := storage["volumeClaimTemplate"].(pulumi.Map)
	claim, _ := template["spec"].(pulumi.Map)
	assert.Equal(t, pulumi.String("200Gi"), resourceRequest(t, claim))
}

func TestEveryPersistentComponentUsesTheCSIStorageClass(t *testing.T) {
	t.Parallel()

	// A component that silently falls back to the default storage class ends
	// up on a volume nobody provisioned, and stays Pending.
	prometheus := PrometheusValues(DefaultRetention, DefaultMetricsSize)

	grafana, ok := prometheus["grafana"].(pulumi.Map)
	require.True(t, ok)

	grafanaPersistence, ok := grafana["persistence"].(pulumi.Map)
	require.True(t, ok)
	assert.Equal(t, pulumi.String(platform.StorageClass), grafanaPersistence["storageClassName"])

	alertmanager, ok := prometheus["alertmanager"].(pulumi.Map)
	require.True(t, ok)

	alertmanagerSpec, _ := alertmanager["alertmanagerSpec"].(pulumi.Map)
	storage, _ := alertmanagerSpec["storage"].(pulumi.Map)
	template, _ := storage["volumeClaimTemplate"].(pulumi.Map)
	claim, _ := template["spec"].(pulumi.Map)
	assert.Equal(t, pulumi.String(platform.StorageClass), claim["storageClassName"])

	loki, _ := LokiValues()["singleBinary"].(pulumi.Map)
	lokiPersistence, _ := loki["persistence"].(pulumi.Map)
	assert.Equal(t, pulumi.String(platform.StorageClass), lokiPersistence["storageClass"])

	tempoPersistence, _ := TempoValues()["persistence"].(pulumi.Map)
	assert.Equal(t, pulumi.String(platform.StorageClass), tempoPersistence["storageClassName"])
}

func TestLokiValues_RunsOneTopologyNotTwo(t *testing.T) {
	t.Parallel()

	// SingleBinary mode plus non-zero read/write/backend replicas deploys both
	// topologies at once — which starts, and then behaves strangely.
	values := LokiValues()

	assert.Equal(t, pulumi.String("SingleBinary"), values["deploymentMode"])

	for _, component := range []string{"read", "write", "backend"} {
		section, ok := values[component].(pulumi.Map)
		require.True(t, ok, component)
		assert.Equal(t, pulumi.Int(0), section["replicas"], component)
	}
}

func TestAlloyValues_ShipsLogsToTheLokiGateway(t *testing.T) {
	t.Parallel()

	// The write endpoint has to match the Service the Loki chart creates in
	// SingleBinary mode. A wrong host here means logs are collected and
	// silently dropped.
	values := AlloyValues()

	alloy, ok := values["alloy"].(pulumi.Map)
	require.True(t, ok)

	configMap, ok := alloy["configMap"].(pulumi.Map)
	require.True(t, ok)

	content, ok := configMap["content"].(pulumi.String)
	require.True(t, ok)

	assert.Contains(t, string(content), observability.LokiGateway)
	assert.Contains(t, string(content), "/loki/api/v1/push")
}

func TestAlloyValues_RunsOnEveryNode(t *testing.T) {
	t.Parallel()

	// Log collection is per-node work; a Deployment would collect from
	// whichever node it happened to land on.
	controller, ok := AlloyValues()["controller"].(pulumi.Map)
	require.True(t, ok)

	assert.Equal(t, pulumi.String("daemonset"), controller["type"])
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
	alloy, ok := AlloyValues()["alloy"].(pulumi.Map)
	require.True(t, ok)

	assert.NotContains(t, alloy, "mounts",
		"a host mount puts this DaemonSet outside Pod Security baseline, and nothing reads one")
}

func TestGrafanaValues_RegistersLokiAndTempoDatasources(t *testing.T) {
	t.Parallel()

	// Registered here rather than by their own charts, so three charts do not
	// race to write one datasource list.
	grafana, ok := PrometheusValues(DefaultRetention, DefaultMetricsSize)["grafana"].(pulumi.Map)
	require.True(t, ok)

	sources, ok := grafana["additionalDataSources"].(pulumi.Array)
	require.True(t, ok)
	require.Len(t, sources, 2)

	types := map[string]bool{}

	for _, source := range sources {
		fields, ok := source.(pulumi.Map)
		require.True(t, ok)

		sourceType, ok := fields["type"].(pulumi.String)
		require.True(t, ok)

		types[string(sourceType)] = true
	}

	assert.Equal(t, map[string]bool{"loki": true, "tempo": true}, types)
}

func prometheusSpec(t *testing.T) pulumi.Map {
	t.Helper()

	prometheus, ok := PrometheusValues(DefaultRetention, DefaultMetricsSize)["prometheus"].(pulumi.Map)
	require.True(t, ok)

	spec, ok := prometheus["prometheusSpec"].(pulumi.Map)
	require.True(t, ok)

	return spec
}

func resourceRequest(t *testing.T, claim pulumi.Map) pulumi.String {
	t.Helper()

	resources, ok := claim["resources"].(pulumi.Map)
	require.True(t, ok)

	requests, ok := resources["requests"].(pulumi.Map)
	require.True(t, ok)

	size, ok := requests["storage"].(pulumi.String)
	require.True(t, ok)

	return size
}

func TestGrafanaDataSources_UseThePinnedEndpoints(t *testing.T) {
	t.Parallel()

	grafana, ok := PrometheusValues(DefaultRetention, DefaultMetricsSize)["grafana"].(pulumi.Map)
	require.True(t, ok)

	sources, ok := grafana["additionalDataSources"].(pulumi.Array)
	require.True(t, ok)

	urls := map[string]string{}

	for _, source := range sources {
		fields, ok := source.(pulumi.Map)
		require.True(t, ok)

		sourceType, ok := fields["type"].(pulumi.String)
		require.True(t, ok)

		url, ok := fields["url"].(pulumi.String)
		require.True(t, ok)

		urls[string(sourceType)] = string(url)
	}

	assert.Equal(t, observability.LokiGateway, urls["loki"])
	assert.Equal(t, observability.TempoHTTP, urls["tempo"])
}

func TestLokiValues_StaysOnTheFilesystem(t *testing.T) {
	t.Parallel()

	loki, ok := LokiValues()["loki"].(pulumi.Map)
	require.True(t, ok)

	storage, ok := loki["storage"].(pulumi.Map)
	require.True(t, ok)
	assert.Equal(t, pulumi.String("filesystem"), storage["type"])
	assert.NotContains(t, storage, "bucketNames")

	// No retention: the volume bounds itself by filling up, and
	// retention_enabled without a bucket would delete logs for no reason.
	assert.NotContains(t, loki, "compactor")
	assert.NotContains(t, loki, "limits_config")
}

func TestLokiValues_SchemaAgreesWithStorage(t *testing.T) {
	t.Parallel()

	// The schema decides where Loki reads chunks, storage decides where it
	// writes them. Disagreeing produces a Loki that ingests happily and
	// returns nothing.
	loki, ok := LokiValues()["loki"].(pulumi.Map)
	require.True(t, ok)

	schema, ok := loki["schemaConfig"].(pulumi.Map)
	require.True(t, ok)

	configs, ok := schema["configs"].(pulumi.Array)
	require.True(t, ok)
	require.Len(t, configs, 1)

	entry, ok := configs[0].(pulumi.Map)
	require.True(t, ok)

	storage, ok := loki["storage"].(pulumi.Map)
	require.True(t, ok)

	assert.Equal(t, pulumi.String("filesystem"), entry["object_store"])
	assert.Equal(t, entry["object_store"], storage["type"])
}

func TestTempoValues_LeavesStorageToTheChart(t *testing.T) {
	t.Parallel()

	// The chart's defaults are already the local backend on the volume, so
	// this sets no storage key at all. Restating a default is a values diff
	// that renders identically — noise in a review, and one more line to keep
	// in step with the chart.
	//
	tempo, ok := TempoValues()["tempo"].(pulumi.Map)
	require.True(t, ok)

	assert.NotContains(t, tempo, "storage")
	assert.Equal(t, pulumi.String("168h"), tempo["retention"])
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

func TestPrometheusValues_PutsNodeExporterWhereItCanRun(t *testing.T) {
	t.Parallel()

	// Talos enables Pod Security Admission with `enforce: baseline` for every
	// namespace except kube-system, and node-exporter needs hostNetwork,
	// hostPID and hostPath volumes. In observability its pods are not created
	// at all — the DaemonSet reports DESIRED 1, CURRENT 0 — and Helm waits out
	// its whole timeout with every other workload in the release Ready. That
	// cost two failed deploys before one event on the DaemonSet explained it.
	exporter, ok := PrometheusValues(DefaultRetention, DefaultMetricsSize)["prometheus-node-exporter"].(pulumi.Map)
	require.True(t, ok, "the subchart's values must be set, or it lands in the release namespace")

	assert.Equal(t, pulumi.String(NodeExporterNamespace), exporter["namespaceOverride"])
	assert.Contains(t, hetzner.PodSecurityExemptNamespaces, NodeExporterNamespace,
		"node-exporter must install where Talos exempts Pod Security Admission")
}

func TestRenderValues_AgreeWithTheLayer(t *testing.T) {
	t.Parallel()

	// tools/charts renders with its own values file, so the render check only
	// sees what that file says. A namespace override stated in the layer and
	// not there would make the check pass on a chart the cluster then refuses,
	// which is the failure this whole pair exists to prevent.
	raw, err := os.ReadFile("../../tools/charts/values/kube-prometheus-stack.yaml")
	require.NoError(t, err, "the render check needs these values to see the override")

	assert.Contains(t, string(raw), "namespaceOverride: "+NodeExporterNamespace,
		"tools/charts/values disagrees with this layer about where node-exporter installs")
}
