package values

// This file holds the shape each values template is executed against, and
// representative data for rendering one without a cluster.
//
// The structs live beside the templates rather than in the layers that fill
// them, for two reasons. A template and the fields it names are one thing, so
// renaming a field should be a compile error in the same package. And
// `task charts:render-check` has to render every template offline — it has no
// cluster to read a CIDR from, so it renders with Probe.

import (
	"fmt"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/platform"
)

// Traefik is pkg/values/traefik.yaml.tmpl.
type Traefik struct {
	Replicas int
	// Name and Location are what the cloud controller manager builds the load
	// balancer from.
	Name     string
	Location string
	// LoadBalancerType is the Hetzner type, from stack config.
	LoadBalancerType string
	// NodeSubnet is the range Traefik trusts a PROXY protocol header from.
	NodeSubnet string
}

// Cilium is pkg/values/cilium.yaml.tmpl.
type Cilium struct {
	// PodCIDR is the range Cilium routes natively.
	PodCIDR string
	// APIHost and APIPort point Cilium at KubePrism on the node, so the CNI
	// does not depend on one control-plane node's life.
	APIHost string
	APIPort int
	// OperatorReplicas is capped at one per control-plane node: each binds a
	// host port, so a second cannot share a node.
	OperatorReplicas int
}

// CCM is pkg/values/hcloud-ccm.yaml.tmpl.
type CCM struct {
	// PodCIDR is what the route controller programmes routes for.
	PodCIDR string
	// SecretName is the Secret both hcloud charts read credentials from.
	SecretName string
}

// MetricsServer is pkg/values/metrics-server.yaml.tmpl.
type MetricsServer struct {
	// AddressTypes pins kubelet address resolution to the node's internal
	// address, which is what its certificate carries.
	AddressTypes string
	Replicas     int
}

// Prometheus is pkg/values/kube-prometheus-stack.yaml.tmpl.
type Prometheus struct {
	Retention        string
	MetricsSize      string
	AlertmanagerSize string
	GrafanaSize      string
	// StorageClass is the class the CSI driver registers.
	StorageClass string
	// LokiURL and TempoURL are registered as Grafana datasources here rather
	// than by their own charts, so one list exists instead of three charts
	// racing to write it.
	LokiURL  string
	TempoURL string
	// NodeExporterNamespace is the one namespace Talos exempts from Pod
	// Security Admission, and the only place a node metrics exporter can run.
	NodeExporterNamespace string
}

// Loki is pkg/values/loki.yaml.tmpl.
type Loki struct {
	StorageClass string
	Size         string
}

// Tempo is pkg/values/tempo.yaml.tmpl.
type Tempo struct {
	StorageClass string
	Size         string
	Retention    string
}

// Alloy is pkg/values/alloy.yaml.tmpl.
type Alloy struct {
	// Config is the collector configuration, indented into a values string by
	// the template.
	Config string
}

// ArgoCD is pkg/values/argo-cd.yaml.tmpl.
type ArgoCD struct {
	// Domain is empty until DNS exists, and an empty one installs Argo CD
	// with no Ingress — the right shape before there is a name to publish
	// under, since the UI is then reachable with `kubectl port-forward` and
	// nothing is exposed by accident.
	Domain string
	// IngressClass is the class the ingress layer registers.
	IngressClass string
	// Issuer is the ClusterIssuer that signs the certificate.
	Issuer string
	// Replicas is how many of each stateless component to run.
	Replicas int
}

// probes is representative data per chart, for rendering offline.
//
// Documentation ranges and obviously-placeholder names on purpose: what the
// render check verifies is that the keys reach the chart's output, and a real
// cluster's addresses in a check would be a second place they live.
var probes = map[string]any{
	"traefik": Traefik{
		Replicas: 2, Name: "probe-ingress", Location: "hel1",
		LoadBalancerType: "lb11", NodeSubnet: "192.0.2.0/24",
	},
	"cilium": Cilium{
		PodCIDR: "198.51.100.0/24", APIHost: "localhost", APIPort: 7445,
		OperatorReplicas: 2,
	},
	"hcloud-ccm":       CCM{PodCIDR: "198.51.100.0/24", SecretName: "hcloud"},
	"metrics-server":   MetricsServer{AddressTypes: "--kubelet-preferred-address-types=InternalIP", Replicas: 2},
	"cert-manager":     nil,
	"external-secrets": nil,
	"kube-prometheus-stack": Prometheus{
		Retention: "30d", MetricsSize: "50Gi", AlertmanagerSize: "5Gi",
		GrafanaSize: "10Gi", StorageClass: platform.StorageClass,
		LokiURL:               "http://loki-gateway.observability.svc.cluster.local",
		TempoURL:              "http://tempo.observability.svc.cluster.local:3200",
		NodeExporterNamespace: "kube-system",
	},
	// A domain on purpose: the Ingress block is conditional, and rendering
	// without one would leave the branch that publishes the UI unchecked.
	"argo-cd": ArgoCD{
		Domain: "argocd.example.com", IngressClass: platform.IngressClass,
		Issuer: "letsencrypt", Replicas: 2,
	},
	"loki":  Loki{StorageClass: platform.StorageClass, Size: "50Gi"},
	"tempo": Tempo{StorageClass: platform.StorageClass, Size: "20Gi", Retention: "168h"},
	"alloy": Alloy{Config: "logging {\n  level = \"info\"\n}\n"},
}

// Probe returns representative data for a chart's template.
//
// An unknown chart is an error rather than nil: nil renders a template whose
// fields all resolve to nothing, which is a values file full of empty strings
// and a check that passes on it.
func Probe(chart string) (any, error) {
	data, known := probes[chart]
	if !known {
		return nil, fmt.Errorf("no probe data for chart %q: add it beside the template", chart)
	}

	return data, nil
}
