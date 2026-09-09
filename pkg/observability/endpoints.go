// Package observability holds the in-cluster endpoints and the collector
// configuration the 60-observability layer deploys.
//
// It is a package rather than constants in the layer because two things need
// them: the layer, which passes them to Helm, and `task observability:check`,
// which feeds the collector configuration to a real Alloy binary. A check
// holding its own copy would validate something the cluster never runs.
package observability

import "fmt"

// In-cluster endpoints Grafana and Alloy address.
//
// These are the charts' names and ports, not ours. Pinned by a test and
// verified against the rendered charts by `task charts:render-check`, because a
// wrong one does not fail the apply — it produces a platform that comes up and
// then cannot query traces or ship logs.
const (
	// LokiGateway serves on port 80, so the URL carries no port.
	LokiGateway = "http://loki-gateway.observability.svc.cluster.local"

	// TempoHTTP is 3200. The chart's Service exposes no 3100 at all — the port
	// it names tempo-prom-metrics is Tempo's HTTP listener, serving the query
	// API Grafana uses as well as metrics.
	TempoHTTP = "http://tempo.observability.svc.cluster.local:3200"
)

// AlloyConfig returns the collector configuration: discover pods, read their
// logs, relabel with Kubernetes metadata, ship to Loki.
//
// Kept as text because Alloy's configuration is a language in its own right.
// Building it from Go values would be harder to read than the thing it
// replaces, and impossible to paste into a running Alloy to debug.
//
// The write endpoint is interpolated rather than written twice, so the address
// Alloy pushes to and the one Grafana reads from cannot drift apart.
func AlloyConfig() string {
	return fmt.Sprintf(alloyConfigTemplate, LokiGateway)
}

const alloyConfigTemplate = `
discovery.kubernetes "pods" {
  role = "pod"
}

discovery.relabel "pods" {
  targets = discovery.kubernetes.pods.targets

  rule {
    source_labels = ["__meta_kubernetes_namespace"]
    target_label  = "namespace"
  }
  rule {
    source_labels = ["__meta_kubernetes_pod_name"]
    target_label  = "pod"
  }
  rule {
    source_labels = ["__meta_kubernetes_pod_container_name"]
    target_label  = "container"
  }
  rule {
    source_labels = ["__meta_kubernetes_namespace", "__meta_kubernetes_pod_label_app_kubernetes_io_name"]
    separator     = "/"
    target_label  = "job"
  }
}

loki.source.kubernetes "pods" {
  targets    = discovery.relabel.pods.output
  forward_to = [loki.write.default.receiver]
}

loki.write "default" {
  endpoint {
    url = "%s/loki/api/v1/push"
  }
}
`
