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
	})
}
