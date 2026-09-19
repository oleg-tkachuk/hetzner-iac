package clusterspec

import (
	"fmt"
)

// Label keys stamped on every hcloud resource this package creates.
//
// They are not decoration. LabelCluster is what the firewall's label selector
// matches, which is how a node created later inherits the perimeter without a
// diff on the firewall itself, and how `hcloud server list -l` scopes to one
// cluster on a shared project.
const (
	LabelCluster   = "cluster"
	LabelRole      = "role"
	LabelPool      = "pool"
	LabelManagedBy = "managed-by"

	RoleControlPlane = "control-plane"
	RoleWorker       = "worker"

	// ManagedBy is the value LabelManagedBy carries: this repository made the
	// resource. Exported because tools/orphans reads it to tell a resource
	// Pulumi owns from one something else created — the two load balancers
	// here have no CCM label and are not orphans.
	ManagedBy = Name
)

// ResourceLabels builds the label set for a cluster-scoped resource. extra
// wins over the defaults so a caller can override a role, but never the
// cluster name — that would detach the resource from its firewall.
func ResourceLabels(cluster string, extra map[string]string) map[string]string {
	labels := map[string]string{
		LabelCluster:   cluster,
		LabelManagedBy: ManagedBy,
	}

	for key, value := range extra {
		if key == LabelCluster {
			continue
		}

		labels[key] = value
	}

	return labels
}

// ClusterSelector is the label selector matching every server in a cluster.
func ClusterSelector(cluster string) string {
	return fmt.Sprintf("%s=%s", LabelCluster, cluster)
}

// Labels stamped on the Talos snapshot, and the value of the first.
//
// These are a contract between two programs that never call each other:
// `task cluster:image:bake` writes them, and lookupTalosImage selects on them.
// They were a format string in each — spelled identically by luck — until this
// file was given the single copy both now build from.
const (
	LabelOS           = "os"
	LabelTalosVersion = "talos-version"

	OSTalos = "talos"
)

// TalosImageSelector is the label selector that finds the baked snapshot for a
// Talos version.
//
// Deliberately no architecture term. The architecture is not a label on the
// snapshot: Hetzner records it as a first-class field, which both sides filter
// on separately — `hcloud image list --architecture` when baking, and
// GetImage's WithArchitecture when looking up. A label would be a second copy
// of a fact the API already holds, free to disagree with it.
func TalosImageSelector(version string) string {
	return fmt.Sprintf("%s=%s,%s=%s", LabelOS, OSTalos, LabelTalosVersion, version)
}
