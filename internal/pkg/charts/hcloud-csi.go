package charts

import "github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"

// HcloudCSI provisions and attaches Hetzner volumes, and registers the two
// storage classes internal/pkg/platform names — one per class of data.
//
// Only the node plugin is listed: the controller is a Deployment the chart
// names after the release, and it is asserted through the smoke check that a
// claim reaches Bound, which is the property that actually matters.
// HcloudCSI is the registry key, and what a layer names when it installs this
// chart. Exported because a layer writing the key as a literal is the drift
// this package exists to remove.
const HcloudCSI = "hcloud-csi"

func init() {
	register(Definition{
		Key:   HcloudCSI,
		Layer: platform.LayerNodePlatform,
		Chart: Chart{
			Name:      "hcloud-csi",
			Repo:      "https://charts.hetzner.cloud",
			Version:   "2.23.0",
			Namespace: NamespaceKubeSystem,
		},
		Workloads: []Object{
			{Kind: DaemonSet, Name: "hcloud-csi-node"},
		},
	})
}
