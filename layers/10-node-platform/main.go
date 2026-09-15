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
	"encoding/base64"
	"fmt"
	"strconv"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/chartsettings"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/clusterref"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/cni"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/hetzner"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/layer"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/platform"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/values"

	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumix"
)

const (
	// CredentialsSecret is the name both charts default to reading.
	CredentialsSecret = "hcloud"
	// SystemNamespace is where both charts install.
	SystemNamespace = "kube-system"
	// StorageClass is re-exported for convenience; pkg/platform owns the name
	// because every claim in the cluster has to ask for the same one, and a
	// mismatch is not rejected — it leaves the volume Pending with nothing
	// saying why.
	StorageClass = platform.StorageClass
)

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
		// The CNI is a choice, not a constant — see pkg/cni. The component
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
		Chart:   "hcloud-ccm",
		Release: "hcloud-cloud-controller-manager",
		After:   []string{CNIComponent, CredentialsSecret},
		ValuesYAML: func(r *layer.Runner) (pulumi.AssetOrArchiveArrayInput, error) {
			return values.Asset("hcloud-ccm", CCMData(r.Cluster.PodCIDR)), nil
		},
	},
	{
		// CSI after the CCM: the driver registers against nodes, and a node
		// still carrying the uninitialized taint has no provider ID to
		// register against.
		Chart: "hcloud-csi",
		After: []string{CredentialsSecret, "hcloud-ccm"},
		// Until this, the chart ran on its defaults — which set no resources,
		// so all eight of its containers were unbounded on a node that also
		// runs etcd. The values carry measured requests and memory limits.
		ValuesYAML: func(*layer.Runner) (pulumi.AssetOrArchiveArrayInput, error) {
			return values.Static("hcloud-csi", nil)
		},
	},
}

// KubePrismHost is where Cilium reaches the API server: KubePrism listens on
// the node itself, so the CNI does not depend on one control-plane node's
// life. The port is chartsettings.KubePrismPort, shared with the machine
// config that enables it.
const KubePrismHost = "localhost"

// CNIComponent is the fixed name of whichever CNI is installed, so the
// components that follow it name the role rather than the implementation.
const CNIComponent = "cni"

// createCNI installs the CNI this stack asks for.
//
// A Create component rather than a Chart one because the chart key is not
// known until the config is read, and because the choice has to be checked
// against what the cluster tier did to kube-proxy before anything is created.
func createCNI(r *layer.Runner, dependencies []pulumi.Resource) (pulumi.Resource, error) {
	name := r.StringOr("cni", cni.Default)

	chosen, err := cni.Select(name, hetzner.KubeProxyDisabled)
	if err != nil {
		return nil, err
	}

	r.Log.Step("cni", name)

	return r.Release(layer.ReleaseArgs{
		Chart:          chosen.Chart,
		TimeoutSeconds: CiliumTimeoutSeconds,
		ValuesYAML:     values.Asset(chosen.Chart, CiliumData(r.Cluster.PodCIDR, r.Cluster.ControlPlaneCount, r.Cluster.RoutingMode)),
	}, layer.DependsOn(dependencies)...)
}

// base64Of encodes a value for a Secret's data field. Secretness survives the
// apply, so a token stays marked as one.
func base64Of(value pulumi.StringOutput) pulumi.StringOutput {
	return value.ApplyT(func(raw string) string {
		return base64.StdEncoding.EncodeToString([]byte(raw))
	}).(pulumi.StringOutput)
}

// createCredentials makes the Secret both hcloud charts read.
func createCredentials(r *layer.Runner, dependencies []pulumi.Resource) (pulumi.Resource, error) {
	return corev1.NewSecret(r.Ctx, CredentialsSecret, &corev1.SecretArgs{
		Metadata: &metav1.ObjectMetaArgs{
			Name:      pulumi.String(CredentialsSecret),
			Namespace: pulumi.String(SystemNamespace),
		},
		// Data, base64, not StringData. stringData is write-only: Kubernetes
		// folds it into data, and the provider records data in the state — so
		// a program setting stringData diffs against its own last apply and
		// plans to replace the Secret, every time, for ever. This apply is the
		// last one that replaces it.
		Data: pulumi.StringMap{
			"token": base64Of(resolveToken(r)),
			// The route controller programmes pod routes inside this network.
			// Without it the CCM starts and silently manages no routes, which
			// surfaces as pods unable to reach pods on other nodes.
			"network": base64Of(r.Cluster.NetworkID.ApplyT(strconv.Itoa).(pulumi.StringOutput)),
		},
	}, r.With(layer.DependsOn(dependencies)...)...)
}

func main() {
	layer.Run(func(r *layer.Runner) error {
		deployed, err := r.Deploy(Components)
		if err != nil {
			return err
		}

		cilium, ok := deployed.Release(CNIComponent)
		if !ok {
			return fmt.Errorf("the cni was not deployed")
		}

		r.Ctx.Export(OutputCNIReady, cilium.Status.Status())
		r.Ctx.Export(OutputStorageClass, pulumi.String(StorageClass))

		return nil
	})
}

// OperatorReplicasWanted is how many Cilium operator replicas to run wherever
// there is somewhere to run them.
const OperatorReplicasWanted = 2

// operatorReplicas caps the operator at one replica per control-plane node.
//
// Each replica binds a host port, so two cannot share a node: on a single-node
// cluster the second stays Pending for ever, reporting `didn't have free ports
// for the requested pod ports` and leaving a permanently red pod in
// `kubectl get pods -A` — which teaches a reader to ignore red pods.
//
// No absent case: pkg/clusterref gates every output on the producer's contract
// version, so by the time a count arrives here the tier has been established
// to publish one. A count below one is a broken producer, and sizing against
// it would scale a working operator to nothing.
func operatorReplicas(controlPlaneCount int) int {
	if controlPlaneCount < 1 {
		return OperatorReplicasWanted
	}

	return min(controlPlaneCount, OperatorReplicasWanted)
}

// The cilium values live in pkg/values/cilium.yaml.tmpl.
//
// It is a named function rather than an inline literal so the settings that
// are coupled to decisions made in the cluster tier can be asserted in a test.
// A mismatch in any of them does not fail an apply — it produces a cluster
// that comes up and then misbehaves:
//
//   - kubeProxyReplacement must be on, because Talos was configured with
//     kube-proxy disabled. Without it there is no service dataplane at all and
//     every ClusterIP silently blackholes.
//   - k8sServiceHost points at KubePrism on localhost rather than at the API
//     server. KubePrism is a node-local load balancer over the control plane,
//     so Cilium keeps working while a control-plane node is replaced.
//   - Native routing depends on the hcloud CCM's route controller, enabled in
//     this layer. With routes but no native routing the packets are
//     encapsulated for no reason; with native routing but no routes they are
//     dropped.
//
// CiliumData resolves what the cilium template needs.
//
// Separated from the component so a test can render the template without a
// Pulumi run.
func CiliumData(
	podCIDR pulumi.StringInput,
	controlPlaneCount pulumi.IntInput,
	routingMode pulumi.StringInput,
) pulumi.Output {
	return pulumi.All(podCIDR, controlPlaneCount, routingMode).ApplyT(func(resolved []any) any {
		return values.Cilium{
			PodCIDR:          resolved[0].(string),
			APIHost:          KubePrismHost,
			APIPort:          chartsettings.KubePrismPort,
			OperatorReplicas: operatorReplicas(resolved[1].(int)),
			RoutingMode:      resolved[2].(string),
		}
	})
}

// resolveToken decides where the Hetzner API token comes from.
//
// The cluster tier exports it, so this layer normally needs no copy of its
// own: one token, set once, in the stack whose provider already holds it.
// This reverses an earlier decision to keep a second copy here — the argument
// against was that exporting a cloud credential puts it into the state of
// every stack holding a reference, which is true and already the case: the
// same channel carries the cluster-admin kubeconfig and the talosconfig, both
// strictly more powerful than an API token.
//
// The config key stays as an override. A cluster stack applied before that
// export existed has nothing to offer, and an operator may deliberately want
// a token scoped differently from the one that built the cluster.
//
// An empty token from either source fails here rather than reaching the
// cluster. Left alone it becomes a Secret that authenticates against nothing,
// and the symptom is a CCM that starts, logs 401 and never clears the
// uninitialized taint — which reads as a broken cluster rather than as a
// missing credential.
func resolveToken(r *layer.Runner) pulumi.StringOutput {
	if r.Cfg.Get("hcloudToken") != "" {
		r.Log.Done("token", "from this layer's config, overriding the cluster stack")

		return r.Cfg.RequireSecret("hcloudToken")
	}

	r.Log.Step("token", "from the cluster stack")

	// Empty rather than absent: pkg/clusterref's version gate has established
	// that the tier publishes this output, and the tier exports it empty when
	// its own `hcloud:token` is unset — a cluster built from an environment
	// variable rather than from stack config. That is a real state with two
	// remedies, not a migration to wait out.
	return pulumix.Cast[pulumi.StringOutput](pulumix.ApplyErr(r.Cluster.HcloudToken,
		func(token string) (string, error) {
			if token == "" {
				return "", fmt.Errorf(
					"the cluster stack exports an empty %q. Either set it there and apply the tier:\n"+
						"  pulumi -C infra/cluster config set --secret hcloud:token <token>\n"+
						"or give this layer its own:\n"+
						"  pulumi config set --secret node-platform:hcloudToken <token>",
					clusterref.OutputHcloudToken)
			}

			return token, nil
		}))
}

// CCMData resolves what the cloud-controller-manager template needs.
func CCMData(podCIDR pulumi.StringInput) pulumi.Output {
	return podCIDR.ToStringOutput().ApplyT(func(cidr string) any {
		return values.CCM{PodCIDR: cidr, SecretName: CredentialsSecret}
	})
}
