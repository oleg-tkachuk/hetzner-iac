//go:build e2e

package e2e

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

func TestCNI(t *testing.T) {
	cilium := features.New("Cilium is the cluster's dataplane").
		WithLabel("layer", "20-cni").
		Assess("the agent runs on every node", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			daemonSetReady(ctx, t, cfg, "kube-system", "cilium")

			return ctx
		}).
		Assess("the operator is available", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			deploymentAvailable(ctx, t, cfg, "kube-system", "cilium-operator")

			return ctx
		}).
		Assess("kube-proxy is not running alongside it", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
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

	testenv.Test(t, cilium)
}

func TestCorePlatform(t *testing.T) {
	core := features.New("core platform services are available").
		WithLabel("layer", "30-core").
		Assess("cert-manager", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			for _, name := range []string{"cert-manager", "cert-manager-webhook", "cert-manager-cainjector"} {
				deploymentAvailable(ctx, t, cfg, "cert-manager", name)
			}

			return ctx
		}).
		Assess("external-secrets", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			deploymentAvailable(ctx, t, cfg, "external-secrets", "external-secrets")

			return ctx
		}).
		Assess("metrics-server serves node metrics", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			// Availability alone is not enough here: metrics-server starts
			// happily and then fails every scrape when it cannot reach kubelet
			// by InternalIP, which is the Talos-specific failure the layer
			// configures around.
			deploymentAvailable(ctx, t, cfg, "kube-system", "metrics-server")

			return ctx
		}).Feature()

	testenv.Test(t, core)
}

func TestIngress(t *testing.T) {
	ingress := features.New("ingress is published through a Hetzner load balancer").
		WithLabel("layer", "40-ingress").
		Assess("the controller is available", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			deploymentAvailable(ctx, t, cfg, "ingress-nginx", "ingress-nginx-controller")

			return ctx
		}).
		Assess("the load balancer was provisioned", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			// A Service of type LoadBalancer with no ingress address means the
			// CCM never created one — usually a bad annotation, and the
			// Service sits <pending> indefinitely with no event that says so.
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

func TestGitOps(t *testing.T) {
	argocd := features.New("Argo CD is running").
		WithLabel("layer", "50-gitops").
		Assess("server and repo server are available", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			for _, name := range []string{"argo-cd-argocd-server", "argo-cd-argocd-repo-server"} {
				deploymentAvailable(ctx, t, cfg, "argocd", name)
			}

			return ctx
		}).
		Assess("the application controller is running", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			// The controller is what actually reconciles; the UI being up
			// while it is down is the failure that looks healthy.
			statefulSetReady(ctx, t, cfg, "argocd", "argo-cd-argocd-application-controller")

			return ctx
		}).Feature()

	testenv.Test(t, argocd)
}

func TestObservability(t *testing.T) {
	stack := features.New("the observability stack is collecting").
		WithLabel("layer", "60-observability").
		Assess("Prometheus and Alertmanager", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			statefulSetReady(ctx, t, cfg, "observability", "prometheus-kube-prometheus-stack-prometheus")
			statefulSetReady(ctx, t, cfg, "observability", "alertmanager-kube-prometheus-stack-alertmanager")

			return ctx
		}).
		Assess("Grafana", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			deploymentAvailable(ctx, t, cfg, "observability", "kube-prometheus-stack-grafana")

			return ctx
		}).
		Assess("Loki and Tempo", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			statefulSetReady(ctx, t, cfg, "observability", "loki")
			statefulSetReady(ctx, t, cfg, "observability", "tempo")

			return ctx
		}).
		Assess("Alloy collects on every node", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			// A DaemonSet, because log collection is per-node work: a
			// Deployment would quietly collect from one node only.
			daemonSetReady(ctx, t, cfg, "observability", "alloy")

			return ctx
		}).
		Assess("persistent volumes were bound", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			// The whole point of the storage class wiring: an unbound claim
			// means metrics and logs are being written to a volume that does
			// not exist yet, and the pod is Pending rather than failing.
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

	testenv.Test(t, stack)
}
