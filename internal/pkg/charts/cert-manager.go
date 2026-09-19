package charts

import "github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"

// CertManager issues the cluster's certificates, and its webhook is in the
// admission path for every Certificate — which is why losing it stops renewal
// silently, and the failure arrives ninety days later.
//
// The chart tags with a leading `v` where most here do not, and confusing the
// two produces a version that does not exist.
// CertManager is the registry key, and what a layer names when it installs this
// chart. Exported because a layer writing the key as a literal is the drift
// this package exists to remove.
const CertManager = "cert-manager"

func init() {
	register(Definition{
		Key:   CertManager,
		Layer: platform.LayerClusterServices,
		Chart: Chart{
			Name:       "cert-manager",
			Repo:       "https://charts.jetstack.io",
			Version:    "v1.21.2", // app v1.21.2
			AppVersion: "v1.21.2",
			Namespace:  CertManager,
		},
		Workloads: []Object{
			{Kind: Deployment, Name: CertManager},
			{Kind: Deployment, Name: "cert-manager-webhook"},
			{Kind: Deployment, Name: "cert-manager-cainjector"},
		},
	})
}
