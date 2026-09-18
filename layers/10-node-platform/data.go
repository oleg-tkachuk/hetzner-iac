// The data half of this layer: what each chart's values template is rendered
// with, and the numbers only these functions read.
//
// Split out of main.go for the reason create.go was.
package main

import (
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/values"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

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
// No absent case: internal/pkg/clusterref gates every output on the producer's contract
// version, so by the time a count arrives here the tier has been established
// to publish one. A count below one is a broken producer, and sizing against
// it would scale a working operator to nothing.
func operatorReplicas(controlPlaneCount int) int {
	if controlPlaneCount < 1 {
		return OperatorReplicasWanted
	}

	return min(controlPlaneCount, OperatorReplicasWanted)
}

// The cilium values live in internal/pkg/values/cilium.yaml.tmpl.
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
			APIPort:          clusterspec.KubePrismPort,
			OperatorReplicas: operatorReplicas(resolved[1].(int)),
			RoutingMode:      resolved[2].(string),
		}
	})
}

// CSIData resolves what the hcloud-csi template needs.
//
// Separated from the component so a test can render the template without a
// Pulumi run, the same shape as CiliumData and CCMData.
func CSIData(location pulumi.StringInput) pulumi.Output {
	return location.ToStringOutput().ApplyT(func(name string) any {
		return values.HcloudCSI{Location: name}
	})
}

// CCMData resolves what the cloud-controller-manager template needs.
func CCMData(podCIDR pulumi.StringInput) pulumi.Output {
	return podCIDR.ToStringOutput().ApplyT(func(cidr string) any {
		return values.CCM{PodCIDR: cidr, SecretName: CredentialsSecret}
	})
}
