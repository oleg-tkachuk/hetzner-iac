package hetzner

import "github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"

// Pulumi resource type tokens for the component resources in this package.
//
// The three-part pkg:module:Type shape is what Pulumi expects; it decides how
// the resource is displayed and how state records it, so these strings are
// part of the state contract. Renaming one after an apply orphans every
// resource under it — use pulumi.Aliases instead.
//
// The package half comes from clusterspec.Name rather than being typed here,
// because cmd/target has to recognise these same tokens to tell one of this
// repository's components from a provider's resource of the same name.
const (
	typeNetwork      = clusterspec.Name + ":cluster:Network"
	typeFirewall     = clusterspec.Name + ":cluster:Firewall"
	typeControlPlane = clusterspec.Name + ":cluster:ControlPlane"
	typeWorkerPool   = clusterspec.Name + ":cluster:WorkerPool"
	typeCluster      = clusterspec.Name + ":cluster:Cluster"
)
