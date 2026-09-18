// The create half of this layer: the components that make something which is
// not a Helm release, and the two helpers only they use.
//
// Split out of main.go, which now holds the entry point and the component
// table, and from data.go, which holds what the charts are rendered with.
// Three jobs that were interleaved, with main() sitting between two of them.
package main

import (
	"encoding/base64"
	"fmt"
	"strconv"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterref"
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

// base64Of encodes a value for a Secret's data field. Secretness survives the
// apply, so a token stays marked as one.
func base64Of(value pulumi.StringOutput) pulumi.StringOutput {
	return value.ApplyT(func(raw string) string {
		return base64.StdEncoding.EncodeToString([]byte(raw))
	}).(pulumi.StringOutput)
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
			"token": base64Of(resolveToken(r)),
			// The route controller programmes pod routes inside this network.
			// Without it the CCM starts and silently manages no routes, which
			// surfaces as pods unable to reach pods on other nodes.
			"network": base64Of(r.Cluster.NetworkID.ApplyT(strconv.Itoa).(pulumi.StringOutput)),
		},
	}, r.With(layer.DependsOn(dependencies)...)...)
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

	// Empty rather than absent: internal/pkg/clusterref's version gate has established
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
