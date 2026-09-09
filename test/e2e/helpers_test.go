//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/e2e-framework/klient/k8s"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

// readyTimeout is generous on purpose. A cluster under test may be pulling
// images onto cold nodes, and a flaky suite is one nobody trusts enough to
// act on.
const readyTimeout = 5 * time.Minute

// deploymentAvailable waits for a Deployment to report available replicas.
func deploymentAvailable(ctx context.Context, t *testing.T, cfg *envconf.Config, namespace, name string) {
	t.Helper()

	client := cfg.Client()

	deployment := appsv1.Deployment{}
	deployment.Name = name
	deployment.Namespace = namespace

	if err := wait.For(
		conditions.New(client.Resources()).DeploymentAvailable(name, namespace),
		wait.WithTimeout(readyTimeout),
		wait.WithContext(ctx),
	); err != nil {
		t.Fatalf("deployment %s/%s never became available: %v", namespace, name, err)
	}
}

// daemonSetReady waits until every scheduled pod of a DaemonSet is ready.
//
// Not the same as "the DaemonSet exists": a CNI DaemonSet that is present but
// has zero ready pods is exactly the state a broken Cilium configuration
// produces.
func daemonSetReady(ctx context.Context, t *testing.T, cfg *envconf.Config, namespace, name string) {
	t.Helper()

	client := cfg.Client()

	daemonSet := &appsv1.DaemonSet{}
	daemonSet.Name = name
	daemonSet.Namespace = namespace

	if err := wait.For(
		conditions.New(client.Resources()).ResourceMatch(daemonSet, func(object k8s.Object) bool {
			set, ok := object.(*appsv1.DaemonSet)
			if !ok {
				return false
			}

			return set.Status.DesiredNumberScheduled > 0 &&
				set.Status.NumberReady == set.Status.DesiredNumberScheduled
		}),
		wait.WithTimeout(readyTimeout),
		wait.WithContext(ctx),
	); err != nil {
		t.Fatalf("daemonset %s/%s never became ready: %v", namespace, name, err)
	}
}

// statefulSetReady waits for every replica of a StatefulSet to be ready.
func statefulSetReady(ctx context.Context, t *testing.T, cfg *envconf.Config, namespace, name string) {
	t.Helper()

	client := cfg.Client()

	statefulSet := &appsv1.StatefulSet{}
	statefulSet.Name = name
	statefulSet.Namespace = namespace

	if err := wait.For(
		conditions.New(client.Resources()).ResourceMatch(statefulSet, func(object k8s.Object) bool {
			set, ok := object.(*appsv1.StatefulSet)
			if !ok {
				return false
			}

			return set.Status.Replicas > 0 && set.Status.ReadyReplicas == set.Status.Replicas
		}),
		wait.WithTimeout(readyTimeout),
		wait.WithContext(ctx),
	); err != nil {
		t.Fatalf("statefulset %s/%s never became ready: %v", namespace, name, err)
	}
}

// listNodes returns every node in the cluster.
func listNodes(ctx context.Context, t *testing.T, cfg *envconf.Config) *corev1.NodeList {
	t.Helper()

	nodes := &corev1.NodeList{}
	if err := cfg.Client().Resources().List(ctx, nodes); err != nil {
		t.Fatalf("list nodes: %v", err)
	}

	if len(nodes.Items) == 0 {
		t.Fatal("cluster reports no nodes at all")
	}

	return nodes
}

// nodeReady reports whether a node's Ready condition is true.
func nodeReady(node corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}

	return false
}

// describeTaints renders a node's taints for a failure message. A test that
// says "node not ready" without saying why costs another round trip.
func describeTaints(node corev1.Node) string {
	if len(node.Spec.Taints) == 0 {
		return "(none)"
	}

	out := ""
	for i, taint := range node.Spec.Taints {
		if i > 0 {
			out += ", "
		}

		out += fmt.Sprintf("%s=%s:%s", taint.Key, taint.Value, taint.Effect)
	}

	return out
}

// fmtSscan parses a dotted-quad address. Wrapped so the caller reads as intent
// rather than as a format string.
func fmtSscan(address string, a, b, c, d *int) (int, error) {
	return fmt.Sscanf(address, "%d.%d.%d.%d", a, b, c, d)
}
