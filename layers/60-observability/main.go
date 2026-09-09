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
	"github.com/oleg-tkachuk/hetzner-iac/pkg/layer"
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
			Values:         LokiValues(),
		}, afterPrometheus); err != nil {
			return err
		}

		if _, err := r.Release(r.Ctx, layer.ReleaseArgs{
			Chart:  "tempo",
			Values: TempoValues(),
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
// SingleBinary rather than the distributed topology: one Loki process on a
// persistent volume is the right size for a single cluster's logs, and the
// distributed mode's read/write/backend split only pays off at a scale where
// object storage is also required.
func LokiValues() pulumi.Map {
	return pulumi.Map{
		"deploymentMode": pulumi.String("SingleBinary"),
		"loki": pulumi.Map{
			"auth_enabled": pulumi.Bool(false),
			"commonConfig": pulumi.Map{"replication_factor": pulumi.Int(1)},
			// Filesystem storage on a persistent volume. Pointing this at
			// Hetzner Object Storage later is a values change, not a
			// redeployment.
			"storage": pulumi.Map{"type": pulumi.String("filesystem")},
			"schemaConfig": pulumi.Map{
				"configs": pulumi.Array{
					pulumi.Map{
						"from":         pulumi.String("2024-04-01"),
						"store":        pulumi.String("tsdb"),
						"object_store": pulumi.String("filesystem"),
						"schema":       pulumi.String("v13"),
						"index": pulumi.Map{
							"prefix": pulumi.String("index_"),
							"period": pulumi.String("24h"),
						},
					},
				},
			},
		},
		"singleBinary": pulumi.Map{
			"replicas": pulumi.Int(1),
			"persistence": pulumi.Map{
				"enabled":      pulumi.Bool(true),
				"storageClass": pulumi.String(StorageClass),
				"size":         pulumi.String("50Gi"),
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
func TempoValues() pulumi.Map {
	return pulumi.Map{
		"persistence": pulumi.Map{
			"enabled":          pulumi.Bool(true),
			"storageClassName": pulumi.String(StorageClass),
			"size":             pulumi.String("20Gi"),
		},
		"tempo": pulumi.Map{
			"retention": pulumi.String("168h"),
		},
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
