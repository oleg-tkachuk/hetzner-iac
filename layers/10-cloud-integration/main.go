// Command cloud-integration installs the Hetzner cloud controller manager and
// the CSI driver.
//
// It runs first. Talos sets cloud-provider=external, which leaves every node
// carrying the node.cloudprovider.kubernetes.io/uninitialized taint until a
// cloud controller manager clears it — so until this layer is applied, nothing
// schedules anywhere. That taint is not an obstacle to work around: it is what
// stops workloads landing on a node before its addresses and routes exist.
//
// The CSI driver's pods stay Pending until 20-cni installs the CNI. The CCM is
// unaffected, because it runs on host networking.
package main

import (
	"strconv"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/layer"

	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

const (
	// CredentialsSecret is the name both charts default to reading.
	CredentialsSecret = "hcloud"
	// SystemNamespace is where both charts install.
	SystemNamespace = "kube-system"
	// StorageClass is what the CSI driver registers.
	StorageClass = "hcloud-volumes"
)

func main() {
	layer.Run(func(r *layer.Runner) error {
		cfg := config.New(r.Ctx, "cloud-integration")

		// The token is read here rather than exported by the cluster tier. A
		// stack that exports a cloud credential puts it into the state of
		// every stack that references it — and holding a reference safely is
		// exactly what layers are supposed to be able to do.
		token := cfg.RequireSecret("hcloudToken")

		// Both charts read the same secret. Creating it once here, rather than
		// letting each chart template its own, keeps one copy of the
		// credential in the cluster instead of two.
		credentials, err := corev1.NewSecret(r.Ctx, CredentialsSecret, &corev1.SecretArgs{
			Metadata: &metav1.ObjectMetaArgs{
				Name:      pulumi.String(CredentialsSecret),
				Namespace: pulumi.String(SystemNamespace),
			},
			StringData: pulumi.StringMap{
				"token": token,
				// The route controller programmes pod routes inside this
				// network. Without it the CCM starts and silently manages no
				// routes, which surfaces as pods unable to reach pods on
				// other nodes.
				"network": r.Cluster.NetworkID.ApplyT(strconv.Itoa).(pulumi.StringOutput),
			},
		}, r.Options...)
		if err != nil {
			return err
		}

		ccm, err := r.Release(r.Ctx, layer.ReleaseArgs{
			Chart:  "hcloud-ccm",
			Name:   "hcloud-cloud-controller-manager",
			Values: CCMValues(r.Cluster.PodCIDR),
		}, pulumi.DependsOn([]pulumi.Resource{credentials}))
		if err != nil {
			return err
		}

		// CSI after the CCM: the driver registers against nodes, and a node
		// still carrying the uninitialized taint has no provider ID to
		// register against.
		if _, err := r.Release(r.Ctx, layer.ReleaseArgs{
			Chart: "hcloud-csi",
			Name:  "hcloud-csi",
		}, pulumi.DependsOn([]pulumi.Resource{credentials, ccm})); err != nil {
			return err
		}

		r.Ctx.Export("storageClass", pulumi.String(StorageClass))

		return nil
	})
}

// CCMValues builds the cloud-controller-manager values.
func CCMValues(podCIDR pulumi.StringInput) pulumi.Map {
	return pulumi.Map{
		"networking": pulumi.Map{
			// Route controller on: the CCM writes a route per node into the
			// private network, which is what lets Cilium use native routing
			// instead of an overlay.
			"enabled":     pulumi.Bool(true),
			"clusterCIDR": podCIDR,
		},
		"env": pulumi.Map{
			"HCLOUD_TOKEN":   SecretRef("token"),
			"HCLOUD_NETWORK": SecretRef("network"),
		},
	}
}

// SecretRef renders the env-var-from-secret shape both charts expect.
func SecretRef(key string) pulumi.Map {
	return pulumi.Map{
		"valueFrom": pulumi.Map{
			"secretKeyRef": pulumi.Map{
				"name": pulumi.String(CredentialsSecret),
				"key":  pulumi.String(key),
			},
		},
	}
}
