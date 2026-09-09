//go:build e2e

package e2e

import (
	"context"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/workloads"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

// TestWorkloads checks every object the platform is expected to run.
//
// The list comes from pkg/workloads, the same table `task charts:render-check`
// proves the charts still produce. That check runs offline in seconds; this one
// runs against a cluster. A rename caught by the first never reaches the second.
func TestWorkloads(t *testing.T) {
	byChart := map[string][]workloads.Workload{}
	for _, w := range workloads.Expected {
		byChart[w.Chart] = append(byChart[w.Chart], w)
	}

	for _, chart := range workloads.Charts() {
		expected := byChart[chart]

		feature := features.New(chart+" is running").
			WithLabel("chart", chart)

		for _, w := range expected {
			workload := w // captured per assessment

			feature = feature.Assess(string(workload.Kind)+" "+workload.Name,
				func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
					switch workload.Kind {
					case workloads.Deployment:
						deploymentAvailable(ctx, t, cfg, workload.Namespace, workload.Name)
					case workloads.StatefulSet:
						statefulSetReady(ctx, t, cfg, workload.Namespace, workload.Name)
					case workloads.DaemonSet:
						daemonSetReady(ctx, t, cfg, workload.Namespace, workload.Name)
					default:
						t.Fatalf("unknown workload kind %q", workload.Kind)
					}

					return ctx
				})
		}

		testenv.Test(t, feature.Feature())
	}
}

func TestCNIReplacesKubeProxy(t *testing.T) {
	noKubeProxy := features.New("kube-proxy is not running alongside Cilium").
		WithLabel("layer", "20-cni").
		Assess("no kube-proxy DaemonSet", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			// Talos was configured with kube-proxy disabled because Cilium
			// replaces it in eBPF. Both running means two components
			// programming the same service dataplane, and the symptoms are
			// intermittent rather than immediate — which is why this is worth
			// asserting rather than assuming.
			sets := &appsv1.DaemonSetList{}
			if err := cfg.Client().Resources("kube-system").List(ctx, sets); err != nil {
				t.Fatalf("list daemonsets: %v", err)
			}

			for _, set := range sets.Items {
				if set.Name == "kube-proxy" {
					t.Error("kube-proxy is running, but Cilium was installed with kubeProxyReplacement enabled")
				}
			}

			return ctx
		}).Feature()

	testenv.Test(t, noKubeProxy)
}

func TestIngressLoadBalancer(t *testing.T) {
	ingress := features.New("ingress is published through a Hetzner load balancer").
		WithLabel("layer", "40-ingress").
		Assess("the load balancer was provisioned", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			// A Service of type LoadBalancer with no ingress address means the
			// CCM never created one — usually a bad annotation, and the
			// Service sits <pending> indefinitely with no event saying so.
			service := &corev1.Service{}
			if err := cfg.Client().Resources("ingress-nginx").
				Get(ctx, "ingress-nginx-controller", "ingress-nginx", service); err != nil {
				t.Fatalf("get ingress service: %v", err)
			}

			if service.Spec.Type != corev1.ServiceTypeLoadBalancer {
				t.Fatalf("ingress service is %s, not LoadBalancer", service.Spec.Type)
			}

			if len(service.Status.LoadBalancer.Ingress) == 0 {
				t.Error("ingress Service has no load balancer address — the hcloud CCM did not provision one")
			}

			return ctx
		}).Feature()

	testenv.Test(t, ingress)
}

func TestObservabilityStorage(t *testing.T) {
	storage := features.New("observability persists its data").
		WithLabel("layer", "60-observability").
		Assess("every claim is bound", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			// The point of the storage-class wiring: an unbound claim means
			// metrics and logs are being written to a volume that does not
			// exist, and the pod is Pending rather than failing.
			claims := &corev1.PersistentVolumeClaimList{}
			if err := cfg.Client().Resources("observability").List(ctx, claims); err != nil {
				t.Fatalf("list persistent volume claims: %v", err)
			}

			if len(claims.Items) == 0 {
				t.Fatal("no persistent volume claims in observability — nothing is persisting")
			}

			for _, claim := range claims.Items {
				if claim.Status.Phase != corev1.ClaimBound {
					t.Errorf("claim %s is %s, not Bound — check the hcloud CSI driver",
						claim.Name, claim.Status.Phase)
				}
			}

			return ctx
		}).Feature()

	testenv.Test(t, storage)
}
