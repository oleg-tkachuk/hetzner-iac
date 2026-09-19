package charts

import "github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"

// Traefik is the ingress controller, and how anything reaches this cluster
// from outside: the Hetzner load balancer 40-ingress creates forwards to the
// node ports internal/pkg/platform pins, and Traefik answers on them.
// Traefik is the registry key, and what a layer names when it installs this
// chart. Exported because a layer writing the key as a literal is the drift
// this package exists to remove.
const Traefik = "traefik"

func init() {
	register(Definition{
		Key:   Traefik,
		Layer: platform.LayerIngress,
		Chart: Chart{
			Name:       "traefik",
			Repo:       "https://traefik.github.io/charts",
			Version:    "41.6.0", // app v3.7.13
			AppVersion: "v3.7.13",
			Namespace:  Traefik,
		},
		Workloads: []Object{
			{Kind: Deployment, Name: Traefik},
		},
	})
}
