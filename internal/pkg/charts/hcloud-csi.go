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

// HcloudCSIDefaultLocation tells the controller which location to create
// volumes in.
//
// A setting that fails silently, which is why it is named: left empty, the
// chart is still valid and the controller instead discovers its location at
// startup — through the metadata service and an api.hetzner.cloud lookup that
// needs CoreDNS — inside the twenty seconds its liveness probe allows. That
// produced a CrashLoopBackOff on the three-node cluster with nothing logged
// past the driver's start line.
const HcloudCSIDefaultLocation = "hcloudVolumeDefaultLocation"

// HcloudCSIController and HcloudCSINode are the chart's two components, and
// they take different priority classes: the node plugin is a DaemonSet, and
// its loss is a node-level failure rather than a cluster-level one.
const (
	HcloudCSIController = "controller"
	HcloudCSINode       = "node"
)

// HcloudCSIProbeLocation is the location the render check renders with.
//
// A literal rather than clusterref.ProbeLocation, which is where that value
// lives: this package must not reach Pulumi's SDK — TestChartsPackage_StaysALeaf
// refuses it — and clusterref does. So the parity is held by a test rather than
// by an import, the same trade the policy pack makes for the admin ports, and
// TestChartProbeLocation_MatchesTheClusterRef is that test.
//
// It has to be a location the schema and the topology validator both accept:
// the value is passed through to an env var verbatim.
const HcloudCSIProbeLocation = "hel1"

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
		Settings: []Setting{
			{
				Set:    []string{HcloudCSIDefaultLocation + "=" + HcloudCSIProbeLocation},
				Expect: `value: "` + HcloudCSIProbeLocation + `"`,
				Why: "left empty the controller discovers its location at startup, through the " +
					"metadata service and an api.hetzner.cloud lookup that needs CoreDNS, inside " +
					"the twenty seconds its liveness probe allows — a CrashLoopBackOff with " +
					"nothing logged past its start line",
			},
			{
				Set:    []string{HcloudCSINode + "." + PriorityClassName + "=" + PriorityNodeCritical},
				Expect: PriorityLineQuoted(PriorityNodeCritical),
				Why: "the node plugin is what mounts volumes on its node; evicted, every pod " +
					"with a volume there stays Pending with nothing wrong on the volume itself",
			},
			{
				Set:    []string{HcloudCSIController + "." + PriorityClassName + "=" + PriorityClusterCritical},
				Expect: PriorityLineQuoted(PriorityClusterCritical),
				Why:    "the controller is what creates and attaches volumes at all",
			},
		},
	})
}
