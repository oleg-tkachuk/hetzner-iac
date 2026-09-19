package charts

import "github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"

// Cilium is the CNI, and the first thing installed for a reason the
// cloud-integration layer explains: a node without a CNI stays NotReady, so
// nothing above it can be scheduled. It replaces kube-proxy in eBPF, which is
// why the cluster tier disables kube-proxy in the Talos machine config.
//
// Selected through internal/pkg/cni rather than named by the layer directly,
// which is why that package pairs its workloads with this declaration too: a
// component with no Chart field is invisible to layertest, and that is how
// cilium's workloads once stopped being verified.
// Cilium is the registry key, and what a layer names when it installs this
// chart. Exported because a layer writing the key as a literal is the drift
// this package exists to remove.
const Cilium = "cilium"

func init() {
	register(Definition{
		Key:   Cilium,
		Layer: platform.LayerNodePlatform,
		Chart: Chart{
			Name:       "cilium",
			Repo:       "https://helm.cilium.io",
			Version:    "1.20.2", // app 1.20.2
			AppVersion: "1.20.2",
			Namespace:  NamespaceKubeSystem,
		},
		Workloads: []Object{
			{Kind: DaemonSet, Name: Cilium},
			{Kind: Deployment, Name: "cilium-operator"},
		},
	})
}
