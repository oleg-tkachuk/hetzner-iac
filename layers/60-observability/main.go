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
	"github.com/oleg-tkachuk/hetzner-iac/pkg/values"

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

// Volume sizes for the components whose data is not the point of the cluster.
// Metrics are the tunable one, above; these three are sized to be forgotten
// about, and named so a reader does not have to guess what a bare "5Gi" is.
const (
	AlertmanagerVolumeSize = "5Gi"
	GrafanaVolumeSize      = "10Gi"
	LokiVolumeSize         = "50Gi"
	TempoVolumeSize        = "20Gi"
)

// TempoRetention is how long a trace is kept. Shorter than metrics on purpose:
// traces are for a question being asked now.
const TempoRetention = "168h"

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
		ValuesYAML: func(r *layer.Runner) pulumi.AssetOrArchiveArrayInput {
			return static(PrometheusChart, PrometheusData(
				r.StringOr("metricsRetention", DefaultRetention),
				r.StringOr("metricsVolumeSize", DefaultMetricsSize),
			))(r)
		},
	},
	{
		Chart:          "loki",
		TimeoutSeconds: LokiTimeoutSeconds,
		After:          []string{PrometheusChart},
		ValuesYAML:     static("loki", LokiData()),
	},
	{
		Chart:      "tempo",
		After:      []string{PrometheusChart},
		ValuesYAML: static("tempo", TempoData()),
	},
	{
		// Alloy collects logs from every node — a DaemonSet, because log
		// collection is per-node work.
		Chart:      "alloy",
		After:      []string{PrometheusChart},
		ValuesYAML: static("alloy", AlloyData()),
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

// The values themselves are pkg/values/*.yaml.tmpl. What is left here is the
// data each template is executed against, and the reasons for the numbers in
// it.
//
// Four scrape targets are disabled in the Prometheus template, and all four
// for the same reason: under Talos, etcd, the scheduler and the controller
// manager listen on localhost only, and kube-proxy does not run at all
// because Cilium replaced it. Leaving them enabled produces permanently
// failing targets and alerts that fire for ever.

// PrometheusData is what kube-prometheus-stack renders with.
func PrometheusData(retention, metricsSize string) values.Prometheus {
	return values.Prometheus{
		Retention:        retention,
		MetricsSize:      metricsSize,
		AlertmanagerSize: AlertmanagerVolumeSize,
		GrafanaSize:      GrafanaVolumeSize,
		StorageClass:     platform.StorageClass,
		LokiURL:          observability.LokiGateway,
		TempoURL:         observability.TempoHTTP,
		// node-exporter needs host access, and kube-system is the one
		// namespace Talos exempts from Pod Security Admission.
		NodeExporterNamespace: NodeExporterNamespace,
	}
}

// LokiData is what the loki template renders with.
func LokiData() values.Loki {
	return values.Loki{StorageClass: platform.StorageClass, Size: LokiVolumeSize}
}

// TempoData is what the tempo template renders with.
func TempoData() values.Tempo {
	return values.Tempo{
		StorageClass: platform.StorageClass,
		Size:         TempoVolumeSize,
		Retention:    TempoRetention,
	}
}

// AlloyData is what the alloy template renders with.
func AlloyData() values.Alloy {
	return values.Alloy{Config: observability.AlloyConfig()}
}

// static renders a template whose values need nothing resolved.
//
// The render can only fail on a template this repository ships, which is a
// programming error a test catches — so the failure is reported through the
// run rather than returned to a caller that could not act on it.
func static(chart string, data any) func(*layer.Runner) pulumi.AssetOrArchiveArrayInput {
	return func(r *layer.Runner) pulumi.AssetOrArchiveArrayInput {
		rendered, err := values.Static(chart, data)
		if err != nil {
			r.Log.Warn(chart, "values template failed to render: %v", err)

			return nil
		}

		return rendered
	}
}
