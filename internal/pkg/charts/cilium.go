package charts

import (
	"strconv"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"
)

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

// Cilium's keys. The cluster tier disables kube-proxy in the Talos machine
// config, so the replacement is not an optimisation here — without it there is
// no service dataplane at all.
const (
	CiliumKubeProxyReplacement = "kubeProxyReplacement"
	CiliumK8sServiceHost       = "k8sServiceHost"
	CiliumK8sServicePort       = "k8sServicePort"
)

// KubePrismPort is the node-local API load balancer Talos enables in the
// cluster tier's machine config, and the port Cilium is pointed at. The two
// are a pair: change one without the other and the CNI cannot reach the API
// server.
//
// The value is clusterspec's, not this package's, and the difference is what
// the comment here used to get wrong: it claimed the layer, the machine config
// and the render check all read one value, while the machine config held a
// bare 7445 of its own. Two copies and a comment saying otherwise.
const KubePrismPort = clusterspec.KubePrismPort

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
		Settings: []Setting{
			{
				Set:    []string{CiliumKubeProxyReplacement + "=true"},
				Expect: `kube-proxy-replacement: "true"`,
				Why:    "Talos runs with kube-proxy disabled; without the replacement every ClusterIP blackholes",
			},
			{
				Set: []string{
					CiliumK8sServiceHost + "=localhost",
					CiliumK8sServicePort + "=" + strconv.Itoa(KubePrismPort),
				},
				Expect: `value: "` + strconv.Itoa(KubePrismPort) + `"`,
				Why:    "Cilium reaches the API through KubePrism on the node; a wrong port ties it to one control-plane node's life",
			},
		},
	})
}
