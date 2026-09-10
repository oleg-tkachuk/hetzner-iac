// Command observability installs the Grafana stack: Prometheus and
// Alertmanager for metrics, Loki for logs, Tempo for traces, Grafana to read
// all three, and Alloy as the collector.
//
// Alloy rather than Promtail: Promtail is deprecated upstream, and Alloy
// collects logs, metrics and traces in one agent instead of one per signal.
//
// This layer owns the Prometheus operator CRDs, which is why every other layer
// installs its chart with serviceMonitor disabled. A layer that created a
// ServiceMonitor would fail on a cluster where this one is absent, and the
// layers are meant to be independently appliable.
package main

import (
	"fmt"
	"strings"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/layer"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/objectstorage"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/observability"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

// StorageClass is what the CSI driver from 10-cloud-integration registers.
const StorageClass = "hcloud-volumes"

// Defaults for the knobs most likely to be tuned per environment.
const (
	DefaultRetention   = "30d"
	DefaultMetricsSize = "50Gi"

	// DefaultLogsRetention matches the metrics retention above. It is not
	// optional when logs go to a bucket: a persistent volume bounds itself by
	// filling up, and object storage does not bound itself at all.
	DefaultLogsRetention = "720h"
)

func main() {
	layer.Run(func(r *layer.Runner) error {
		cfg := config.New(r.Ctx, "observability")

		retention := cfg.Get("metricsRetention")
		if retention == "" {
			retention = DefaultRetention
		}

		metricsSize := cfg.Get("metricsVolumeSize")
		if metricsSize == "" {
			metricsSize = DefaultMetricsSize
		}

		logsRetention := cfg.Get("logsRetention")
		if logsRetention == "" {
			logsRetention = DefaultLogsRetention
		}

		store, err := resolveObjectStore(r.Ctx, cfg)
		if err != nil {
			return err
		}

		if store == nil {
			// Which of the two shapes this layer is in is the first thing
			// anyone debugging retention or disk pressure needs to know.
			r.Log.Skipped("object-storage",
				"objectStorageStackRef unset, loki and tempo stay on volumes")

			if cfg.Get("logsRetention") != "" {
				// Set, plausible, and inert. Loki's compactor only enforces
				// retention against a bucket, so this silently does nothing.
				r.Log.Warn("retention",
					"logsRetention %s has no effect without objectStorageStackRef", logsRetention)
			}
		} else {
			r.Log.Step("object-storage", "loki chunks and tempo blocks → bucket")
		}

		// kube-prometheus-stack brings the operator, its CRDs, Prometheus,
		// Alertmanager, Grafana and the exporters. One chart, because
		// splitting it means owning operator/CRD version compatibility by
		// hand.
		prometheus, err := r.Release(r.Ctx, layer.ReleaseArgs{
			Chart: "kube-prometheus-stack",
			// The largest chart here: CRDs, an admission webhook whose
			// certificate is generated in a hook Job, and several images.
			TimeoutSeconds: 1200,
			Values:         PrometheusValues(retention, metricsSize),
		})
		if err != nil {
			return err
		}

		// Everything else registers datasources and ServiceMonitors against
		// the operator, so it has to exist first.
		afterPrometheus := pulumi.DependsOn([]pulumi.Resource{prometheus})

		if _, err := r.Release(r.Ctx, layer.ReleaseArgs{
			Chart:          "loki",
			TimeoutSeconds: 900,
			Values:         LokiValues(store, logsRetention),
		}, afterPrometheus); err != nil {
			return err
		}

		if _, err := r.Release(r.Ctx, layer.ReleaseArgs{
			Chart:  "tempo",
			Values: TempoValues(store),
		}, afterPrometheus); err != nil {
			return err
		}

		// Alloy collects logs from every node — a DaemonSet, because log
		// collection is per-node work.
		if _, err := r.Release(r.Ctx, layer.ReleaseArgs{
			Chart:  "alloy",
			Values: AlloyValues(),
		}, afterPrometheus); err != nil {
			return err
		}

		return nil
	})
}

// PrometheusValues builds the kube-prometheus-stack values.
//
// Four scrape targets are disabled, and all four for the same reason: under
// Talos, etcd, the scheduler and the controller manager listen on localhost
// only, and kube-proxy does not run at all because Cilium replaced it. Leaving
// them enabled produces permanently failing scrape targets and alerts that
// fire forever, which is how an alerting stack gets ignored.
func PrometheusValues(retention, metricsSize string) pulumi.Map {
	return pulumi.Map{
		"prometheus": pulumi.Map{
			"prometheusSpec": pulumi.Map{
				"retention": pulumi.String(retention),
				// Persist metrics. The default is an emptyDir, which loses
				// every series when the pod moves — and a pod moving is the
				// normal case, not the exception.
				"storageSpec": pulumi.Map{
					"volumeClaimTemplate": pulumi.Map{
						"spec": persistentVolumeSpec(metricsSize),
					},
				},
				// Discover monitors from every namespace rather than only
				// those carrying this release's labels, which is what lets
				// other layers be scraped without this layer knowing them.
				"serviceMonitorSelectorNilUsesHelmValues": pulumi.Bool(false),
				"podMonitorSelectorNilUsesHelmValues":     pulumi.Bool(false),
				"ruleSelectorNilUsesHelmValues":           pulumi.Bool(false),
			},
		},
		"alertmanager": pulumi.Map{
			"alertmanagerSpec": pulumi.Map{
				"storage": pulumi.Map{
					"volumeClaimTemplate": pulumi.Map{
						"spec": persistentVolumeSpec("5Gi"),
					},
				},
			},
		},
		"grafana": pulumi.Map{
			"enabled": pulumi.Bool(true),
			"persistence": pulumi.Map{
				"enabled":          pulumi.Bool(true),
				"storageClassName": pulumi.String(StorageClass),
				"size":             pulumi.String("10Gi"),
			},
			// Loki and Tempo are registered here rather than by their own
			// charts, so Grafana has one datasource list instead of three
			// charts racing to write it.
			"additionalDataSources": pulumi.Array{
				pulumi.Map{
					"name":   pulumi.String("Loki"),
					"type":   pulumi.String("loki"),
					"url":    pulumi.String(observability.LokiGateway),
					"access": pulumi.String("proxy"),
				},
				pulumi.Map{
					"name":   pulumi.String("Tempo"),
					"type":   pulumi.String("tempo"),
					"url":    pulumi.String(observability.TempoHTTP),
					"access": pulumi.String("proxy"),
				},
			},
			"sidecar": pulumi.Map{
				// Pick up dashboards from ConfigMaps in any namespace, so a
				// workload ships its own dashboard without this layer changing.
				"dashboards": pulumi.Map{
					"enabled":         pulumi.Bool(true),
					"searchNamespace": pulumi.String("ALL"),
				},
			},
		},
		"nodeExporter":          pulumi.Map{"enabled": pulumi.Bool(true)},
		"kubeEtcd":              pulumi.Map{"enabled": pulumi.Bool(false)},
		"kubeScheduler":         pulumi.Map{"enabled": pulumi.Bool(false)},
		"kubeControllerManager": pulumi.Map{"enabled": pulumi.Bool(false)},
		"kubeProxy":             pulumi.Map{"enabled": pulumi.Bool(false)},
	}
}

// LokiValues builds the Loki values.
//
// SingleBinary rather than the distributed topology, with or without a bucket:
// the read/write/backend split is about scaling query and ingest paths
// independently, which one cluster's logs do not need. Object storage is a
// separate question from topology, and answering it does not change this one.
//
// A nil store keeps the filesystem backend and the 50Gi volume that bounds it.
// With a store, the volume holds only the WAL and the index cache, so it is
// smaller, and retention becomes Loki's job rather than the volume's.
func LokiValues(store *objectStore, retention string) pulumi.Map {
	objectStoreType := pulumi.String("filesystem")
	volumeSize := pulumi.String("50Gi")
	storage := pulumi.Map{"type": pulumi.String("filesystem")}

	loki := pulumi.Map{
		"auth_enabled": pulumi.Bool(false),
		"commonConfig": pulumi.Map{"replication_factor": pulumi.Int(1)},
	}

	if store != nil {
		objectStoreType = pulumi.String("s3")
		// WAL and index cache only; the chunks live in the bucket.
		volumeSize = pulumi.String("10Gi")

		storage = pulumi.Map{
			"type": pulumi.String("s3"),
			// One bucket for all three. Loki keys them apart by prefix, and
			// three buckets would be three things to create and grant for no
			// separation anyone here needs.
			"bucketNames": pulumi.Map{
				"chunks": store.Bucket,
				"ruler":  store.Bucket,
				"admin":  store.Bucket,
			},
			"s3": pulumi.Map{
				"endpoint":        store.Endpoint,
				"region":          store.Region,
				"accessKeyId":     store.AccessKey,
				"secretAccessKey": store.SecretKey,
				// Hetzner serves both addressing styles; path style is what
				// 05-object-storage creates the bucket with.
				"s3ForcePathStyle": pulumi.Bool(true),
				"insecure":         pulumi.Bool(false),
			},
		}

		// Retention is the compactor's job, and it does nothing unless asked.
		// Without this the bucket grows for ever — a volume at least stops by
		// filling up.
		loki["compactor"] = pulumi.Map{
			"retention_enabled":    pulumi.Bool(true),
			"delete_request_store": pulumi.String("s3"),
		}
		loki["limits_config"] = pulumi.Map{
			"retention_period": pulumi.String(retention),
		}
	}

	loki["storage"] = storage
	loki["schemaConfig"] = pulumi.Map{
		"configs": pulumi.Array{
			pulumi.Map{
				"from":  pulumi.String("2024-04-01"),
				"store": pulumi.String("tsdb"),
				// Must agree with storage.type above: the schema decides where
				// Loki looks for chunks, and storage decides where it writes
				// them. Disagreeing means writing to one and reading the other.
				"object_store": objectStoreType,
				"schema":       pulumi.String("v13"),
				"index": pulumi.Map{
					"prefix": pulumi.String("index_"),
					"period": pulumi.String("24h"),
				},
			},
		},
	}

	return pulumi.Map{
		"deploymentMode": pulumi.String("SingleBinary"),
		"loki":           loki,
		"singleBinary": pulumi.Map{
			"replicas": pulumi.Int(1),
			"persistence": pulumi.Map{
				"enabled":      pulumi.Bool(true),
				"storageClass": pulumi.String(StorageClass),
				"size":         volumeSize,
			},
		},
		// The distributed components must be off in SingleBinary mode.
		// Leaving them on deploys both topologies at once.
		"read":         pulumi.Map{"replicas": pulumi.Int(0)},
		"write":        pulumi.Map{"replicas": pulumi.Int(0)},
		"backend":      pulumi.Map{"replicas": pulumi.Int(0)},
		"chunksCache":  pulumi.Map{"enabled": pulumi.Bool(false)},
		"resultsCache": pulumi.Map{"enabled": pulumi.Bool(false)},
	}
}

// TempoValues builds the Tempo values.
//
// A nil store keeps the local backend on its volume. With a store, the volume
// holds only the ingester WAL, so it is smaller — Tempo needs it either way,
// because a trace is written to the WAL before it becomes a block.
func TempoValues(store *objectStore) pulumi.Map {
	volumeSize := pulumi.String("20Gi")

	tempo := pulumi.Map{
		"retention": pulumi.String("168h"),
	}

	if store != nil {
		// WAL only; blocks live in the bucket.
		volumeSize = pulumi.String("10Gi")

		tempo["storage"] = pulumi.Map{
			"trace": pulumi.Map{
				"backend": pulumi.String("s3"),
				"s3": pulumi.Map{
					"bucket":   store.Bucket,
					"endpoint": store.Endpoint,
					"region":   store.Region,
					// Snake case, unlike Loki's camel case for the same two
					// values: this map is passed through to Tempo's own config
					// verbatim, so these are Tempo's names, not the chart's.
					"access_key":     store.AccessKey,
					"secret_key":     store.SecretKey,
					"forcepathstyle": pulumi.Bool(true),
					"insecure":       pulumi.Bool(false),
				},
				"wal": pulumi.Map{"path": pulumi.String("/var/tempo/wal")},
			},
		}
		// The chart's default trace.local.path merges back in underneath this
		// and renders even with an s3 backend. Tempo reads only the backend it
		// was told to use, so it is inert — verified by rendering, not assumed.
	}

	// No storage key at all without a store: the chart's defaults are already
	// the local backend, and restating them changes the values without
	// changing a byte of what renders.
	return pulumi.Map{
		"persistence": pulumi.Map{
			"enabled":          pulumi.Bool(true),
			"storageClassName": pulumi.String(StorageClass),
			"size":             volumeSize,
		},
		"tempo": tempo,
	}
}

// AlloyValues builds the Alloy values.
func AlloyValues() pulumi.Map {
	return pulumi.Map{
		"controller": pulumi.Map{"type": pulumi.String("daemonset")},
		"alloy": pulumi.Map{
			"mounts": pulumi.Map{
				// Container logs live here on Talos as on any other node; the
				// mount is what lets Alloy read them.
				"varlog": pulumi.Bool(true),
			},
			"configMap": pulumi.Map{
				"content": pulumi.String(observability.AlloyConfig()),
			},
		},
	}
}

// persistentVolumeSpec is the claim template shape both Prometheus and
// Alertmanager use. Sharing it means the storage class cannot drift between
// two components that must both survive a pod move.
func persistentVolumeSpec(size string) pulumi.Map {
	return pulumi.Map{
		"storageClassName": pulumi.String(StorageClass),
		"accessModes":      pulumi.ToStringArray([]string{"ReadWriteOnce"}),
		"resources": pulumi.Map{
			"requests": pulumi.Map{"storage": pulumi.String(size)},
		},
	}
}

// objectStore is the bucket Loki and Tempo write to, resolved.
//
// nil means no bucket is configured, and both fall back to the persistent
// volume they have always used. That is deliberate: the layers are meant to be
// independently appliable, and requiring the object-storage stack would mean
// this one cannot be applied without it.
type objectStore struct {
	Bucket    pulumi.StringOutput
	Endpoint  pulumi.StringOutput
	Region    pulumi.StringOutput
	AccessKey pulumi.StringOutput
	SecretKey pulumi.StringOutput
}

// resolveObjectStore reads the 05-object-storage stack, if one is configured.
func resolveObjectStore(ctx *pulumi.Context, cfg *config.Config) (*objectStore, error) {
	ref := cfg.Get("objectStorageStackRef")
	if ref == "" {
		return nil, nil
	}

	stack, err := pulumi.NewStackReference(ctx, ref, nil)
	if err != nil {
		return nil, fmt.Errorf("object storage stack reference %q: %w", ref, err)
	}

	// The credentials come from this layer's own configuration rather than
	// from that stack's outputs, for the reason 10-cloud-integration gives
	// about the Hetzner token: a stack that exports a credential puts it into
	// the state of every stack that references it. Only the bucket name, the
	// endpoint and the region cross the boundary.
	var missing []string

	for _, key := range []string{"objectStorageAccessKey", "objectStorageSecretKey"} {
		if _, err := cfg.Try(key); err != nil {
			missing = append(missing, key)
		}
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf(
			"objectStorageStackRef is set but the S3 credentials are not: %s\n"+
				"  pulumi config set --secret observability:objectStorageAccessKey <key>\n"+
				"  pulumi config set --secret observability:objectStorageSecretKey <key>",
			strings.Join(missing, ", "))
	}

	return &objectStore{
		Bucket:    stack.GetStringOutput(pulumi.String(objectstorage.OutputObservabilityBucket)),
		Endpoint:  stack.GetStringOutput(pulumi.String(objectstorage.OutputEndpointHost)),
		Region:    stack.GetStringOutput(pulumi.String(objectstorage.OutputRegion)),
		AccessKey: cfg.GetSecret("objectStorageAccessKey"),
		SecretKey: cfg.GetSecret("objectStorageSecretKey"),
	}, nil
}
