package charts

import "github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"

// MetricsServer serves resource metrics — what `kubectl top` and a
// HorizontalPodAutoscaler on CPU read.
//
// It scrapes the kubelet over TLS and verifies the certificate, so it cannot
// be Ready until the kubelet has one the cluster CA signed: the layer installs
// it after the certificate approver, and without that ordering Helm waits out
// its whole timeout — measured at 611 seconds before rolling back.
// MetricsServer is the registry key, and what a layer names when it installs this
// chart. Exported because a layer writing the key as a literal is the drift
// this package exists to remove.
const MetricsServer = "metrics-server"

// MetricsServerAddressTypes pins kubelet address resolution to the node's
// internal address. Talos kubelet certificates carry that address, and the
// chart default tries the hostname first — metrics-server then starts and
// every scrape fails, so the autoscaler is silently blind.
const MetricsServerAddressTypes = "--kubelet-preferred-address-types=InternalIP"

func init() {
	register(Definition{
		Key:   MetricsServer,
		Layer: platform.LayerClusterServices,
		Chart: Chart{
			Name:       "metrics-server",
			Repo:       "https://kubernetes-sigs.github.io/metrics-server/",
			Version:    "3.14.0", // app 0.9.0
			AppVersion: "0.9.0",
			Namespace:  NamespaceKubeSystem,
		},
		Workloads: []Object{
			{Kind: Deployment, Name: MetricsServer},
		},
		Probe: metricsServerProbe,
		Settings: []Setting{
			{
				Set:    []string{`args[0]=` + MetricsServerAddressTypes},
				Expect: MetricsServerAddressTypes,
				Why: "Talos kubelet certificates carry the internal address; the chart default " +
					"tries the hostname and every scrape fails",
			},
		},
	})
}

// MetricsServerValues is what metrics-server.yaml.tmpl is executed against.
type MetricsServerValues struct {
	// AddressTypes pins kubelet address resolution to the node's internal
	// address, which is what its certificate carries.
	AddressTypes string
	Replicas     int
}

// metricsServerProbe renders the template offline.
func metricsServerProbe() any {
	return MetricsServerValues{AddressTypes: MetricsServerAddressTypes, Replicas: 2}
}
