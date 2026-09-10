package main

import (
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/observability"

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

	assert.Equal(t, pulumi.String(StorageClass), claim["storageClassName"])
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
	assert.Equal(t, pulumi.String(StorageClass), grafanaPersistence["storageClassName"])

	alertmanager, ok := prometheus["alertmanager"].(pulumi.Map)
	require.True(t, ok)

	alertmanagerSpec, _ := alertmanager["alertmanagerSpec"].(pulumi.Map)
	storage, _ := alertmanagerSpec["storage"].(pulumi.Map)
	template, _ := storage["volumeClaimTemplate"].(pulumi.Map)
	claim, _ := template["spec"].(pulumi.Map)
	assert.Equal(t, pulumi.String(StorageClass), claim["storageClassName"])

	loki, _ := LokiValues(nil, DefaultLogsRetention)["singleBinary"].(pulumi.Map)
	lokiPersistence, _ := loki["persistence"].(pulumi.Map)
	assert.Equal(t, pulumi.String(StorageClass), lokiPersistence["storageClass"])

	tempoPersistence, _ := TempoValues(nil)["persistence"].(pulumi.Map)
	assert.Equal(t, pulumi.String(StorageClass), tempoPersistence["storageClassName"])
}

func TestLokiValues_RunsOneTopologyNotTwo(t *testing.T) {
	t.Parallel()

	// SingleBinary mode plus non-zero read/write/backend replicas deploys both
	// topologies at once — which starts, and then behaves strangely.
	values := LokiValues(nil, DefaultLogsRetention)

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

// testStore is a resolved object store with values a test can recognise in
// rendered output.
func testStore() *objectStore {
	return &objectStore{
		Bucket:    pulumi.String("platform-prod-observability").ToStringOutput(),
		Endpoint:  pulumi.String("fsn1.your-objectstorage.com").ToStringOutput(),
		Region:    pulumi.String("fsn1").ToStringOutput(),
		AccessKey: pulumi.String("AKIAEXAMPLE").ToStringOutput(),
		SecretKey: pulumi.String("shhh").ToStringOutput(),
	}
}

func TestLokiValues_WithoutAStoreStaysOnTheFilesystem(t *testing.T) {
	t.Parallel()

	// The layers are independently appliable, so this one has to work with no
	// object-storage stack at all — and unchanged from before it existed.
	loki, ok := LokiValues(nil, DefaultLogsRetention)["loki"].(pulumi.Map)
	require.True(t, ok)

	storage, ok := loki["storage"].(pulumi.Map)
	require.True(t, ok)
	assert.Equal(t, pulumi.String("filesystem"), storage["type"])
	assert.NotContains(t, storage, "bucketNames")

	// No retention either: the volume bounds itself by filling up, and
	// retention_enabled without a bucket would delete logs for no reason.
	assert.NotContains(t, loki, "compactor")
	assert.NotContains(t, loki, "limits_config")
}

func TestLokiValues_WithAStoreWritesToTheBucket(t *testing.T) {
	t.Parallel()

	store := testStore()

	loki, ok := LokiValues(store, "168h")["loki"].(pulumi.Map)
	require.True(t, ok)

	storage, ok := loki["storage"].(pulumi.Map)
	require.True(t, ok)
	assert.Equal(t, pulumi.String("s3"), storage["type"])

	// All three bucket names, because Loki refuses to start with chunks or
	// ruler unset once the backend is s3.
	names, ok := storage["bucketNames"].(pulumi.Map)
	require.True(t, ok)

	for _, key := range []string{"chunks", "ruler", "admin"} {
		assert.Equal(t, store.Bucket, names[key], key)
	}

	s3, ok := storage["s3"].(pulumi.Map)
	require.True(t, ok)
	assert.Equal(t, store.Endpoint, s3["endpoint"])
	assert.Equal(t, store.Region, s3["region"])
	assert.Equal(t, store.AccessKey, s3["accessKeyId"])
	assert.Equal(t, store.SecretKey, s3["secretAccessKey"])
	assert.Equal(t, pulumi.Bool(true), s3["s3ForcePathStyle"])
}

func TestLokiValues_SchemaAgreesWithStorage(t *testing.T) {
	t.Parallel()

	// The schema decides where Loki reads chunks, storage decides where it
	// writes them. Disagreeing produces a Loki that ingests happily and
	// returns nothing.
	for name, tc := range map[string]struct {
		store *objectStore
		want  pulumi.String
	}{
		"filesystem": {nil, pulumi.String("filesystem")},
		"s3":         {testStore(), pulumi.String("s3")},
	} {
		loki, ok := LokiValues(tc.store, DefaultLogsRetention)["loki"].(pulumi.Map)
		require.True(t, ok, name)

		schema, ok := loki["schemaConfig"].(pulumi.Map)
		require.True(t, ok, name)

		configs, ok := schema["configs"].(pulumi.Array)
		require.True(t, ok, name)
		require.Len(t, configs, 1, name)

		entry, ok := configs[0].(pulumi.Map)
		require.True(t, ok, name)

		assert.Equal(t, tc.want, entry["object_store"], name)

		storage, ok := loki["storage"].(pulumi.Map)
		require.True(t, ok, name)
		assert.Equal(t, entry["object_store"], storage["type"], name)
	}
}

func TestLokiValues_BucketRetentionIsSetAndPassedThrough(t *testing.T) {
	t.Parallel()

	// A bucket has no size to fill, so nothing stops it growing except this.
	loki, ok := LokiValues(testStore(), "168h")["loki"].(pulumi.Map)
	require.True(t, ok)

	compactor, ok := loki["compactor"].(pulumi.Map)
	require.True(t, ok)
	assert.Equal(t, pulumi.Bool(true), compactor["retention_enabled"])
	assert.Equal(t, pulumi.String("s3"), compactor["delete_request_store"])

	limits, ok := loki["limits_config"].(pulumi.Map)
	require.True(t, ok)
	assert.Equal(t, pulumi.String("168h"), limits["retention_period"])
}

func TestLokiValues_TheVolumeShrinksWhenChunksLeaveIt(t *testing.T) {
	t.Parallel()

	// Still a volume either way — Loki writes the WAL locally before it
	// writes a chunk anywhere — but 50Gi of it is only needed for chunks.
	sizeOf := func(store *objectStore) pulumi.StringInput {
		single, ok := LokiValues(store, DefaultLogsRetention)["singleBinary"].(pulumi.Map)
		require.True(t, ok)

		persistence, ok := single["persistence"].(pulumi.Map)
		require.True(t, ok)
		assert.Equal(t, pulumi.Bool(true), persistence["enabled"])

		return persistence["size"].(pulumi.StringInput)
	}

	assert.Equal(t, pulumi.String("50Gi"), sizeOf(nil))
	assert.Equal(t, pulumi.String("10Gi"), sizeOf(testStore()))
}

func TestTempoValues_WithoutAStoreLeavesStorageToTheChart(t *testing.T) {
	t.Parallel()

	// The chart's defaults are already the local backend on the volume, so
	// this sets no storage key at all. Restating a default is a values diff
	// that renders identically — noise in a review, and one more line to keep
	// in step with the chart.
	tempo, ok := TempoValues(nil)["tempo"].(pulumi.Map)
	require.True(t, ok)

	assert.NotContains(t, tempo, "storage")
	assert.Equal(t, pulumi.String("168h"), tempo["retention"])
}

func TestTempoValues_WithAStoreWritesToTheBucket(t *testing.T) {
	t.Parallel()

	store := testStore()

	tempo, ok := TempoValues(store)["tempo"].(pulumi.Map)
	require.True(t, ok)

	storage, _ := tempo["storage"].(pulumi.Map)
	trace, ok := storage["trace"].(pulumi.Map)
	require.True(t, ok)

	assert.Equal(t, pulumi.String("s3"), trace["backend"])

	s3, ok := trace["s3"].(pulumi.Map)
	require.True(t, ok)
	assert.Equal(t, store.Bucket, s3["bucket"])
	assert.Equal(t, store.Endpoint, s3["endpoint"])

	// Tempo's own names, not the chart's: this map is passed through to its
	// config verbatim, so accessKeyId here would be silently ignored.
	assert.Equal(t, store.AccessKey, s3["access_key"])
	assert.Equal(t, store.SecretKey, s3["secret_key"])
	assert.Equal(t, pulumi.Bool(true), s3["forcepathstyle"])

	// The WAL path survives the switch. Tempo writes every trace there before
	// it becomes a block, bucket or no bucket.
	wal, ok := trace["wal"].(pulumi.Map)
	require.True(t, ok)
	assert.Equal(t, pulumi.String("/var/tempo/wal"), wal["path"])
}
