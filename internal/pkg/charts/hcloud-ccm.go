package charts

import "github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"

// HcloudCCM is the cloud controller manager: it clears the `uninitialized`
// taint Talos leaves on every node, so nothing schedules until it runs, and it
// programmes the per-node routes pod traffic takes in native routing mode.
//
// The one chart whose release name is not its registry key — the chart is
// published as `hcloud-cloud-controller-manager`, and every object it creates
// carries that name.
// HcloudCCM is the registry key, and what a layer names when it installs this
// chart. Exported because a layer writing the key as a literal is the drift
// this package exists to remove.
const HcloudCCM = "hcloud-ccm"

// hcloudCCMRelease is the chart's own name, and the release name every object
// it creates carries. Three uses, one constant: the chart, the release and the
// Deployment are the same string for the same reason.
const hcloudCCMRelease = "hcloud-cloud-controller-manager"

func init() {
	register(Definition{
		Key:     HcloudCCM,
		Layer:   platform.LayerNodePlatform,
		Release: hcloudCCMRelease,
		Chart: Chart{
			Name:      "hcloud-cloud-controller-manager",
			Repo:      "https://charts.hetzner.cloud",
			Version:   "1.37.0",
			Namespace: NamespaceKubeSystem,
		},
		Workloads: []Object{
			{Kind: Deployment, Name: hcloudCCMRelease},
		},
		Probe: ccmProbe,
		Settings: []Setting{
			{
				Set:    []string{PriorityClassName + "=" + PriorityClusterCritical},
				Expect: PriorityLineQuoted(PriorityClusterCritical),
				Why: "it clears Talos's uninitialized taint, so without it a new or replaced " +
					"node never becomes schedulable",
			},
		},
	})
}

// CCMValues is what hcloud-ccm.yaml.tmpl is executed against.
type CCMValues struct {
	// PodCIDR is what the route controller programmes routes for.
	PodCIDR string
	// SecretName is the Secret both hcloud charts read credentials from.
	SecretName string
}

// ccmProbe renders the template offline.
func ccmProbe() any {
	return CCMValues{PodCIDR: "198.51.100.0/24", SecretName: "hcloud"}
}
