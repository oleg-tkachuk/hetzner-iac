//go:build e2e

package e2e

import (
	"context"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/workloads"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

// TestWorkloads checks every object the platform is expected to run.
//
// The list comes from internal/pkg/workloads, the same table `charts:render-check`
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
		WithLabel("layer", "10-node-platform").
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
		Assess("the service exposes the node ports the load balancer targets", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			// NOT a Service of type LoadBalancer, and this test asserted one
			// until a live run failed on it. The load balancer is created by
			// layers/40-ingress through the Hetzner provider, not by asking
			// the cloud controller manager for one: the CCM refuses to target
			// a node carrying
			// node.kubernetes.io/exclude-from-external-load-balancers, which
			// Talos puts on every control-plane node, and the first live apply
			// produced an address with zero targets.
			//
			// So what is verifiable from inside the cluster is the half the
			// cluster owns: the Service is a NodePort on exactly the two
			// numbers internal/pkg/platform pins, because a Pulumi-managed
			// load balancer cannot forward to a port Kubernetes picked after
			// the fact. A mismatch is the quiet kind — the load balancer comes
			// up, health-checks a closed port, and reports every target
			// unhealthy while nothing in the cluster looks wrong.
			//
			// The load balancer's own existence and address are the ingress
			// layer's stack outputs, which `task platform:outputs
			// layer=40-ingress` reports and Pulumi's own state asserts.
			service := &corev1.Service{}
			if err := cfg.Client().Resources("traefik").
				Get(ctx, "traefik", "traefik", service); err != nil {
				t.Fatalf("get ingress service: %v", err)
			}

			if service.Spec.Type != corev1.ServiceTypeNodePort {
				t.Fatalf("ingress service is %s, not NodePort — the Hetzner load balancer "+
					"forwards to node ports and cannot target a ClusterIP", service.Spec.Type)
			}

			want := map[int32]string{
				platform.IngressNodePortHTTP:  "web",
				platform.IngressNodePortHTTPS: "websecure",
			}

			got := map[int32]string{}
			for _, port := range service.Spec.Ports {
				got[port.NodePort] = port.Name
			}

			for number, name := range want {
				if got[number] != name {
					t.Errorf("node port %d is %q, not the pinned %q — the load balancer "+
						"health-checks %d and would report every target unhealthy",
						number, got[number], name, number)
				}
			}

			return ctx
		}).Feature()

	testenv.Test(t, ingress)
}
