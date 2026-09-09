//go:build e2e

package e2e

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

// uninitializedTaint is what Talos leaves on every node until a cloud
// controller manager clears it.
const uninitializedTaint = "node.cloudprovider.kubernetes.io/uninitialized"

func TestClusterFoundation(t *testing.T) {
	nodesReady := features.New("every node is Ready").
		WithLabel("layer", "cluster").
		Assess("nodes report Ready", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			for _, node := range listNodes(ctx, t, cfg).Items {
				if !nodeReady(node) {
					// A NotReady node on this platform is almost always one of
					// two things, and the taints say which.
					t.Errorf("node %s is not Ready (taints: %s) — no CNI, or the CCM has not initialised it",
						node.Name, describeTaints(node))
				}
			}

			return ctx
		}).Feature()

	taintsCleared := features.New("the cloud controller manager initialised every node").
		WithLabel("layer", "10-cloud-integration").
		Assess("no node still carries the uninitialized taint", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			// This taint is the mechanism that stops workloads landing on a
			// node before its addresses and routes exist. A node still
			// carrying it schedules nothing, and the symptom people report is
			// "my pods are Pending" rather than "the CCM is down".
			for _, node := range listNodes(ctx, t, cfg).Items {
				for _, taint := range node.Spec.Taints {
					if taint.Key == uninitializedTaint {
						t.Errorf("node %s still carries %s — the hcloud CCM is not running or cannot authenticate",
							node.Name, uninitializedTaint)
					}
				}
			}

			return ctx
		}).
		Assess("nodes have a hcloud provider ID", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			// The provider ID is what the CSI driver registers volumes
			// against; without it, volumes attach to nothing.
			for _, node := range listNodes(ctx, t, cfg).Items {
				if node.Spec.ProviderID == "" {
					t.Errorf("node %s has no provider ID — the CCM has not claimed it", node.Name)
				}
			}

			return ctx
		}).Feature()

	privateAddressing := features.New("nodes are addressed on the private network").
		WithLabel("layer", "cluster").
		Assess("every node's InternalIP is private", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			// Talos was configured with kubelet's nodeIP pinned to the node
			// subnet. If a node advertises its public address instead, every
			// intra-cluster connection leaves Hetzner's private network —
			// metered, slower, and exposed.
			for _, node := range listNodes(ctx, t, cfg).Items {
				internal := ""

				for _, address := range node.Status.Addresses {
					if address.Type == corev1.NodeInternalIP {
						internal = address.Address
					}
				}

				if internal == "" {
					t.Errorf("node %s has no InternalIP", node.Name)

					continue
				}

				if !isPrivateIPv4(internal) {
					t.Errorf("node %s advertises %s as its InternalIP — kubelet is not pinned to the private subnet",
						node.Name, internal)
				}
			}

			return ctx
		}).Feature()

	storageClassPresent := features.New("the CSI driver registered its storage class").
		WithLabel("layer", "10-cloud-integration").
		Assess("hcloud-volumes exists", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			// Every persistent component in the observability layer requests
			// this class by name. Without it they stay Pending forever, which
			// reads as a Prometheus problem rather than a CSI one.
			classes := &storagev1.StorageClassList{}
			if err := cfg.Client().Resources().List(ctx, classes); err != nil {
				t.Fatalf("list storage classes: %v", err)
			}

			for _, class := range classes.Items {
				if class.Name == "hcloud-volumes" {
					return ctx
				}
			}

			t.Error("storage class hcloud-volumes is missing — the hcloud CSI driver is not installed")

			return ctx
		}).Feature()

	testenv.Test(t, nodesReady, taintsCleared, privateAddressing, storageClassPresent)
}

// isPrivateIPv4 reports whether an address is in RFC 1918 space.
func isPrivateIPv4(address string) bool {
	var a, b, c, d int

	if _, err := fmtSscan(address, &a, &b, &c, &d); err != nil {
		return false
	}

	switch {
	case a == 10:
		return true
	case a == 172 && b >= 16 && b <= 31:
		return true
	case a == 192 && b == 168:
		return true
	default:
		return false
	}
}
