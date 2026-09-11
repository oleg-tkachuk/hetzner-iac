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
// `task platform:apply-all` walks them. Nothing stopped anyone applying the
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
	"fmt"
	"strconv"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/chartsettings"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/clusterref"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/layer"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/platform"

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
	// because 60-observability's claims have to ask for the same one.
	StorageClass = platform.StorageClass
)

func main() {
	layer.Run(program)
}

// program is the layer, separated from main so a test can run it against a
// mock monitor and assert the ordering this stack exists to guarantee.
func program(r *layer.Runner) error {
	// Cilium first, and everything else after it. This is the ordering
	// that used to be two directory names.
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

	afterCilium := pulumi.DependsOn([]pulumi.Resource{cilium})

	// Both charts read the same secret. Creating it once here, rather than
	// letting each chart template its own, keeps one copy of the
	// credential in the cluster instead of two.
	credentials, err := corev1.NewSecret(r.Ctx, CredentialsSecret, &corev1.SecretArgs{
		Metadata: &metav1.ObjectMetaArgs{
			Name:      pulumi.String(CredentialsSecret),
			Namespace: pulumi.String(SystemNamespace),
		},
		StringData: pulumi.StringMap{
			"token": resolveToken(r),
			// The route controller programmes pod routes inside this
			// network. Without it the CCM starts and silently manages no
			// routes, which surfaces as pods unable to reach pods on
			// other nodes.
			"network": r.Cluster.NetworkID.ApplyT(strconv.Itoa).(pulumi.StringOutput),
		},
	}, r.With(afterCilium)...)
	if err != nil {
		return err
	}

	ccm, err := r.Release(r.Ctx, layer.ReleaseArgs{
		Chart:  "hcloud-ccm",
		Name:   "hcloud-cloud-controller-manager",
		Values: CCMValues(r.Cluster.PodCIDR),
	}, afterCilium, pulumi.DependsOn([]pulumi.Resource{credentials}))
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

	r.Ctx.Export("cniReady", cilium.Status.Status())
	r.Ctx.Export("storageClass", pulumi.String(StorageClass))

	return nil
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
func CiliumValues(podCIDR pulumi.StringInput, controlPlaneCount pulumi.IntInput) pulumi.Map {
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
			"replicas": controlPlaneCount.ToIntOutput().ApplyT(operatorReplicas),
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
