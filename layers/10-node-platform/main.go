// Command node-platform makes a Hetzner server into a working Kubernetes
// node: Cilium for the dataplane, the hcloud cloud controller manager for
// addresses and routes, and the CSI driver for volumes.
//
// # Why these are one stack and not three
//
// They were two, and the split was not free. Talos ships no CNI on purpose, so
// nodes are NotReady until Cilium runs; a NotReady node carries
// node.kubernetes.io/not-ready:NoSchedule, and the CCM chart renders a
// Deployment whose tolerations cover that key only with effect NoExecute. A
// Deployment is given none of its own. So the CCM cannot be scheduled until
// Cilium has made the node Ready — Cilium can, because its agent tolerates
// `operator: Exists` and its operator names both not-ready and uninitialized.
//
// As separate stacks that ordering lived in the directory names and the order
// `task platform:apply layer=all` walks them. Nothing stopped anyone applying the
// cloud-integration layer on its own against a CNI-less cluster, and doing so
// cost a ten-minute apply: the release sat Pending for its whole timeout and
// Helm rolled it back on `atomic`. In one stack the same fact is a DependsOn
// the engine enforces, and applying half of it is not expressible.
//
// Pulumi's own guidance says as much: split along major layers, and reserve a
// stack per component for genuinely independent services. These three are not
// independent — they are one handover, from a server that boots to a node that
// can run a pod.
package main

import (
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/charts"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/layer"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

const (
	// CredentialsSecret is the name both charts default to reading.
	CredentialsSecret = "hcloud"
	// CCMChart and CSIChart are the registry keys, spelled once each so the
	// component table, the values and the namespace below cannot disagree.
	CCMChart = "hcloud-ccm"
	CSIChart = "hcloud-csi"
	// StorageClass is re-exported for convenience; internal/pkg/platform owns the name
	// because every claim in the cluster has to ask for the same one, and a
	// mismatch is not rejected — it leaves the volume Pending with nothing
	// saying why.
	StorageClass = platform.StorageClass
)

// SystemNamespace is where the Secret both hcloud charts read has to live:
// their own namespace, because each chart defaults to reading a Secret named
// `hcloud` beside itself.
//
// Read from the registry rather than spelled here. It was "kube-system" with a
// comment saying "where both charts install" and nothing holding the two
// together — and a Secret in the wrong namespace is not an error: both charts
// install, find no credential, and the CCM logs 401 and never clears the
// uninitialized taint while the CSI cannot provision. That reads as a broken
// cluster rather than as a misplaced Secret.
//
// TestSystemNamespace_IsWhereBothChartsInstall holds the CSI to the same
// answer, since this takes it from the CCM.
var SystemNamespace = charts.MustGet(CCMChart).Namespace

// CiliumTimeoutSeconds is longer than the default: Cilium pulls large images
// onto nodes with nothing cached yet, and every other layer waits on it.
const CiliumTimeoutSeconds = 900

// Stack outputs. Every export is a named constant, in every layer, so the set
// a stack publishes is greppable and a consumer that appears later needs no
// rename. TestLayers_ExportOnlyNamedOutputs holds that.
const (
	OutputCNIReady     = "cniReady"
	OutputStorageClass = "storageClass"
)

// Components are what this layer deploys.
//
// The Secret is a component rather than something created around the set, and
// that is the point of the design: it sits BETWEEN Cilium and the two charts
// that read it. A hook running after the releases — the obvious shape, and the
// one first proposed — could not express that, and a hook running before them
// could not express its own dependency on Cilium. Anything with a place in the
// order has to be in the order.
var Components = layer.Components{
	{
		// The CNI is a choice, not a constant — see internal/pkg/cni. The component
		// keeps a fixed Name so the two charts that follow it do not have to
		// know which implementation was picked.
		Name:           CNIComponent,
		TimeoutSeconds: CiliumTimeoutSeconds,
		Create:         createCNI,
	},
	{
		// Both charts read the same Secret. Creating it once here, rather than
		// letting each chart template its own, keeps one copy of the
		// credential in the cluster instead of two.
		Name:   CredentialsSecret,
		After:  []string{CNIComponent},
		Create: createCredentials,
	},
	{
		Chart:   CCMChart,
		Release: "hcloud-cloud-controller-manager",
		After:   []string{CNIComponent, CredentialsSecret},
		ValuesFrom: func(r *layer.Runner) pulumi.Output {
			return CCMData(r.Cluster.PodCIDR)
		},
	},
	{
		// CSI after the CCM: the driver registers against nodes, and a node
		// still carrying the uninitialized taint has no provider ID to
		// register against.
		Chart: CSIChart,
		After: []string{CredentialsSecret, CCMChart},
		// Until this, the chart ran on its defaults — which set no resources,
		// so all eight of its containers were unbounded on a node that also
		// runs etcd. The values carry measured requests and memory limits.
		//
		// The location comes from the cluster tier rather than being
		// discovered by the controller at startup: see the template, and
		// charts.HcloudCSIDefaultLocation.
		ValuesFrom: func(r *layer.Runner) pulumi.Output {
			return CSIData(r.Cluster.Location)
		},
	},
}

// KubePrismHost is where Cilium reaches the API server: KubePrism listens on
// the node itself, so the CNI does not depend on one control-plane node's
// life. The port is clusterref.KubePrismPort, which is the one value the
// machine config writes and Cilium is pointed at — this comment claimed that
// sharing while machineconfig.go held a literal 7445 of its own.
const KubePrismHost = "localhost"

// CNIComponent is the fixed name of whichever CNI is installed, so the
// components that follow it name the role rather than the implementation.
const CNIComponent = "cni"

func main() {
	layer.Run(func(r *layer.Runner) error {
		deployed, err := r.Deploy(Components)
		if err != nil {
			return err
		}

		cilium, err := deployed.MustRelease(CNIComponent)
		if err != nil {
			return err
		}

		r.Ctx.Export(OutputCNIReady, cilium.Status.Status())
		r.Ctx.Export(OutputStorageClass, pulumi.String(StorageClass))

		return nil
	})
}
