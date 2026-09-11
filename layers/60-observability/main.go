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
	"github.com/oleg-tkachuk/hetzner-iac/pkg/platform"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// NodeExporterNamespace is where the node-exporter DaemonSet installs, and the
// only part of the kube-prometheus-stack release that does not land in
// observability. See the comment beside it in PrometheusValues.
const NodeExporterNamespace = "kube-system"

// Defaults for the knobs most likely to be tuned per environment.
const (
	DefaultRetention   = "30d"
	DefaultMetricsSize = "50Gi"
)

// Chart timeouts, named because a bare number in a table says nothing about
// why that chart is slower than the rest.
const (
	// PrometheusTimeoutSeconds — the largest chart here: CRDs, an admission
	// webhook whose certificate is generated in a hook Job, several images.
	PrometheusTimeoutSeconds = 1200
	// LokiTimeoutSeconds — a StatefulSet with a volume to bind.
	LokiTimeoutSeconds = 900
)

// PrometheusChart is the component everything else follows: it brings the
// Prometheus operator and its CRDs, and Loki, Tempo and Alloy all register
// datasources or ServiceMonitors against them.
const PrometheusChart = "kube-prometheus-stack"

// Components are what this layer deploys.
//
// The ordering is the point. Every component but Prometheus names it in After,
// so the dependency is a fact the engine holds — previously it was a
// pulumi.DependsOn built by hand and passed to three calls, where the fourth
// forgetting it would have produced a race nothing reports until a datasource
// is missing.
var Components = layer.Components{
	{
		// One chart, because splitting it means owning operator/CRD version
		// compatibility by hand.
		Chart:          PrometheusChart,
		TimeoutSeconds: PrometheusTimeoutSeconds,
		Values: func(r *layer.Runner) pulumi.Map {
			return PrometheusValues(
				r.StringOr("metricsRetention", DefaultRetention),
				r.StringOr("metricsVolumeSize", DefaultMetricsSize),
			)
		},
	},
	{
		Chart:          "loki",
		TimeoutSeconds: LokiTimeoutSeconds,
		After:          []string{PrometheusChart},
		Values:         func(*layer.Runner) pulumi.Map { return LokiValues() },
	},
	{
		Chart:  "tempo",
		After:  []string{PrometheusChart},
		Values: func(*layer.Runner) pulumi.Map { return TempoValues() },
	},
	{
		// Alloy collects logs from every node — a DaemonSet, because log
		// collection is per-node work.
		Chart:  "alloy",
		After:  []string{PrometheusChart},
		Values: func(*layer.Runner) pulumi.Map { return AlloyValues() },
	},
}

func main() {
	layer.Run(func(r *layer.Runner) error {
		// The chart's default route ends at a receiver named `null`, so every
		// alert is grouped, inhibited and then dropped. Nothing else in the
		// stack says so: Prometheus stores metrics, rules evaluate, alerts
		// fire, and they reach nobody.
		//
		// Permanent rather than ephemeral, and unconditional rather than
		// behind a config key: it is true of every apply until a receiver
		// exists, and the commit that adds one deletes this.
		r.Log.Warn("alerting",
			"alertmanager has no receiver: alerts are grouped, inhibited and then dropped")

		_, err := r.Deploy(Components)

		return err
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
				"storageClassName": pulumi.String(platform.StorageClass),
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
		"nodeExporter": pulumi.Map{"enabled": pulumi.Bool(true)},

		// node-exporter goes to kube-system, and it is the only part of this
		// release that does.
		//
		// It needs hostNetwork, hostPID and hostPath volumes — that is what a
		// node metrics exporter is — and Talos enables Pod Security Admission
		// with `enforce: baseline` for every namespace except kube-system. In
		// observability its pods are not created at all: the DaemonSet reports
		// DESIRED 1, CURRENT 0, Helm waits out its whole timeout, and the only
		// evidence is one event saying "violates PodSecurity baseline:latest".
		// That cost two failed deploys.
		//
		// kube-system rather than labelling observability privileged, because
		// the label would also exempt Grafana, Prometheus, Alertmanager and
		// kube-state-metrics — four workloads that comply with baseline today.
		// node-exporter is a node-level agent like Cilium, the CCM and the CSI
		// driver, all of which already live there.
		//
		// Prometheus finds it: serviceMonitorSelectorNilUsesHelmValues is
		// false, so monitors are discovered in every namespace.
		"prometheus-node-exporter": pulumi.Map{
			"namespaceOverride": pulumi.String(NodeExporterNamespace),
		},
		"kubeEtcd":              pulumi.Map{"enabled": pulumi.Bool(false)},
		"kubeScheduler":         pulumi.Map{"enabled": pulumi.Bool(false)},
		"kubeControllerManager": pulumi.Map{"enabled": pulumi.Bool(false)},
		"kubeProxy":             pulumi.Map{"enabled": pulumi.Bool(false)},
	}
}

// LokiValues builds the Loki values.
//
// SingleBinary rather than the distributed topology: the read/write/backend
// split is about scaling query and ingest paths independently, which one
// cluster's logs do not need.
//
// The filesystem backend on a 50Gi volume, which is also what bounds
// retention — Loki's compactor only enforces a retention period against
// object storage, so on a volume the bound is the volume filling up.
func LokiValues() pulumi.Map {
	storage := pulumi.Map{"type": pulumi.String("filesystem")}

	loki := pulumi.Map{
		"auth_enabled": pulumi.Bool(false),
		"commonConfig": pulumi.Map{"replication_factor": pulumi.Int(1)},
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
				"object_store": pulumi.String("filesystem"),
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
				"storageClass": pulumi.String(platform.StorageClass),
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
//
// No storage key at all: the chart's defaults are already the local backend on
// the volume, and restating them changes the values without changing a byte of
// what renders. Tempo needs the volume regardless — a trace is written to the
// WAL before it becomes a block.
func TempoValues() pulumi.Map {
	return pulumi.Map{
		"persistence": pulumi.Map{
			"enabled":          pulumi.Bool(true),
			"storageClassName": pulumi.String(platform.StorageClass),
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
		"storageClassName": pulumi.String(platform.StorageClass),
		"accessModes":      pulumi.ToStringArray([]string{"ReadWriteOnce"}),
		"resources": pulumi.Map{
			"requests": pulumi.Map{"storage": pulumi.String(size)},
		},
	}
}
