package charts

import "github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"

// ExternalSecrets reads secrets from outside the cluster and writes them in as
// ordinary Secrets. It reconciles nothing until a store exists — see
// internal/pkg/platform's store constants and the layer that creates one.
//
// Three Deployments run, and one is listed: the controller. The webhook and
// the cert-controller serve admission and certificates inside the cluster, and
// the check that matters for them is the store reaching Ready, which
// internal/pkg/clustersmoke asks.
// ExternalSecrets is the registry key, and what a layer names when it installs this
// chart. Exported because a layer writing the key as a literal is the drift
// this package exists to remove.
const ExternalSecrets = "external-secrets"

func init() {
	register(Definition{
		Key:   ExternalSecrets,
		Layer: platform.LayerClusterServices,
		Chart: Chart{
			Name:       "external-secrets",
			Repo:       "https://charts.external-secrets.io",
			Version:    "2.11.0", // app v2.11.0
			AppVersion: "v2.11.0",
			Namespace:  ExternalSecrets,
		},
		Workloads: []Object{
			{Kind: Deployment, Name: ExternalSecrets},
		},
	})
}
