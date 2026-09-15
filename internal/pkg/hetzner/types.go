package hetzner

// Pulumi resource type tokens for the component resources in this package.
//
// The three-part pkg:module:Type shape is what Pulumi expects; it decides how
// the resource is displayed and how state records it, so these strings are
// part of the state contract. Renaming one after an apply orphans every
// resource under it — use pulumi.Aliases instead.
const (
	typeNetwork      = "hetzner-iac:cluster:Network"
	typeFirewall     = "hetzner-iac:cluster:Firewall"
	typeControlPlane = "hetzner-iac:cluster:ControlPlane"
	typeWorkerPool   = "hetzner-iac:cluster:WorkerPool"
	typeCluster      = "hetzner-iac:cluster:Cluster"
)
