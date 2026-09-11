// Command cni installs Cilium.
//
// The cluster tier ships with no CNI on purpose — Talos would otherwise
// install Flannel, which would then have to be removed before Cilium could
// take over — so nodes are NotReady until this layer runs. That is the
// intended handover point.
package main

import (
	"fmt"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/chartsettings"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/clusterref"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/layer"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

func main() {
	layer.Run(func(r *layer.Runner) error {
		cilium, err := r.Release(r.Ctx, layer.ReleaseArgs{
			Chart: "cilium",
			// Cilium pulls large images onto nodes with nothing cached yet,
			// and every other layer waits on it.
			TimeoutSeconds: 900,
			Values:         CiliumValues(r.Cluster.PodCIDR, r.Cluster.ControlPlaneCount),
		})
		if err != nil {
			return err
		}

		r.Ctx.Export("cniReady", cilium.Status.Status())

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
// nil means the cluster tier has not been applied since it began exporting the
// count. That is an error rather than a default: guessing a replica count from
// an absent fact is how a working operator gets scaled to nothing, and the
// operator who sees this message fixes it in one command.
func operatorReplicas(controlPlaneCount *int) (int, error) {
	if controlPlaneCount == nil {
		return 0, fmt.Errorf(
			"the cluster stack exports no %q, so the Cilium operator cannot be sized:\n"+
				"  task cluster:apply stack=<stack>",
			clusterref.OutputControlPlaneCount)
	}

	if *controlPlaneCount < 1 {
		return 0, fmt.Errorf("the cluster stack reports %d control-plane nodes",
			*controlPlaneCount)
	}

	return min(*controlPlaneCount, OperatorReplicasWanted), nil
}

// CiliumValues builds the chart values.
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
//     20-cloud-integration. With routes but no native routing the packets are
//     encapsulated for no reason; with native routing but no routes they are
//     dropped.
func CiliumValues(podCIDR pulumi.StringInput, controlPlaneCount pulumi.IntPtrInput) pulumi.Map {
	return pulumi.Map{
		"ipam": pulumi.Map{
			// Addresses come from the Kubernetes node spec, which the CCM
			// populates — one source of truth for pod CIDRs rather than
			// Cilium keeping its own.
			"mode": pulumi.String("kubernetes"),
		},

		// These three keys are constants rather than literals: Helm accepts an
		// unknown key silently, so a typo here leaves the chart's default in
		// place and the cluster starts with no service dataplane at all.
		// task charts:render-check asserts their EFFECT on the rendered chart,
		// reading the same constants.
		chartsettings.CiliumKubeProxyReplacement: pulumi.Bool(true),
		chartsettings.CiliumK8sServiceHost:       pulumi.String("localhost"),
		chartsettings.CiliumK8sServicePort:       pulumi.Int(chartsettings.KubePrismPort),

		// Native routing rather than an overlay: the CCM programmes a route
		// per node inside the private network, so pod traffic needs no
		// encapsulation. One fewer header, and readable packet captures.
		"routingMode":           pulumi.String("native"),
		"ipv4NativeRoutingCIDR": podCIDR,
		"autoDirectNodeRoutes":  pulumi.Bool(true),
		"endpointRoutes":        pulumi.Map{"enabled": pulumi.Bool(true)},
		"bpf":                   pulumi.Map{"masquerade": pulumi.Bool(true)},
		"enableIPv4Masquerade":  pulumi.Bool(true),
		"enableIPv6Masquerade":  pulumi.Bool(false),
		"ipv6":                  pulumi.Map{"enabled": pulumi.Bool(false)},
		"loadBalancer":          pulumi.Map{"algorithm": pulumi.String("maglev")},
		"externalIPs":           pulumi.Map{"enabled": pulumi.Bool(true)},
		"nodePort":              pulumi.Map{"enabled": pulumi.Bool(true)},
		"hostPort":              pulumi.Map{"enabled": pulumi.Bool(true)},
		"socketLB":              pulumi.Map{"hostNamespaceOnly": pulumi.Bool(true)},

		// Talos mounts the cgroup filesystem itself and runs a read-only
		// root, so Cilium must not try to mount it and must be granted the
		// capabilities it would otherwise take by running fully privileged.
		"cgroup": pulumi.Map{
			"autoMount": pulumi.Map{"enabled": pulumi.Bool(false)},
			"hostRoot":  pulumi.String("/sys/fs/cgroup"),
		},
		"securityContext": pulumi.Map{
			"capabilities": pulumi.Map{
				"ciliumAgent": pulumi.ToStringArray([]string{
					"CHOWN", "KILL", "NET_ADMIN", "NET_RAW", "IPC_LOCK",
					"SYS_ADMIN", "SYS_RESOURCE", "DAC_OVERRIDE", "FOWNER",
					"SETGID", "SETUID",
				}),
				"cleanCiliumState": pulumi.ToStringArray([]string{
					"NET_ADMIN", "SYS_ADMIN", "SYS_RESOURCE",
				}),
			},
		},

		// Two operator replicas so reconciliation of Cilium's own custom
		// resources survives a node failure — but only where two can run. See
		// operatorReplicas.
		"operator": pulumi.Map{
			"replicas": controlPlaneCount.ToIntPtrOutput().
				ApplyT(operatorReplicas).(pulumi.IntOutput),
			"prometheus": pulumi.Map{
				"enabled": pulumi.Bool(true),
				// ServiceMonitors belong to the observability layer, which
				// owns the Prometheus operator CRDs. Creating one here would
				// make this layer fail on a cluster where that layer is not
				// installed — and the layers are meant to be independent.
				"serviceMonitor": pulumi.Map{"enabled": pulumi.Bool(false)},
			},
		},
		"prometheus": pulumi.Map{
			"enabled":        pulumi.Bool(true),
			"serviceMonitor": pulumi.Map{"enabled": pulumi.Bool(false)},
		},

		"hubble": pulumi.Map{
			"enabled": pulumi.Bool(true),
			"relay":   pulumi.Map{"enabled": pulumi.Bool(true)},
			"ui":      pulumi.Map{"enabled": pulumi.Bool(true)},
			"metrics": pulumi.Map{
				"enabled": pulumi.ToStringArray([]string{
					"dns", "drop", "tcp", "flow", "port-distribution", "icmp",
				}),
				"serviceMonitor": pulumi.Map{"enabled": pulumi.Bool(false)},
			},
		},
	}
}
