// The create half of this layer: the components that make something which is
// not a Helm release, and the two helpers only they use.
//
// Split out of main.go, which now holds the entry point and the component
// table, and from data.go, which holds what the charts are rendered with.
// Three jobs that were interleaved, with main() sitting between two of them.
package main

import (
	"strconv"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/cni"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/layer"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/values"

	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumix"
)

// createCNI installs the CNI this stack asks for.
//
// A Create component rather than a Chart one because the chart key is not
// known until the config is read, and because the choice has to be checked
// against what the cluster tier did to kube-proxy before anything is created.
func createCNI(r *layer.Runner, dependencies []pulumi.Resource) (pulumi.Resource, error) {
	name := r.StringOr("cni", cni.Default)

	chosen, err := cni.Select(name, clusterspec.KubeProxyDisabled)
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
			"token": layer.Base64Of(r.HcloudToken()),
			// The route controller programmes pod routes inside this network.
			// Without it the CCM starts and silently manages no routes, which
			// surfaces as pods unable to reach pods on other nodes.
			"network": layer.Base64Of(pulumix.Cast[pulumi.StringOutput](pulumix.Apply(r.Cluster.NetworkID, strconv.Itoa))),
		},
	}, r.With(layer.DependsOn(dependencies)...)...)
}
