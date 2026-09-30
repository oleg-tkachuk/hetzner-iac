package layer

import (
	"fmt"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterref"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumix"
)

// HcloudTokenKey is the stack config key a layer may override the cluster
// tier's Hetzner token with. Deliberately not declared in any Pulumi.yaml —
// the layers that read it say why in theirs.
//
// #nosec G101 -- the name of a config key, not a credential.
const HcloudTokenKey = "hcloudToken"

// HcloudToken decides where a layer's Hetzner API token comes from.
//
// The cluster tier exports it, so a layer normally needs no copy of its own:
// one token, set once, in the stack whose provider already holds it. Exporting
// a cloud credential puts it into the state of every stack holding a
// reference, which is true and already the case: the same channel carries the
// cluster-admin kubeconfig and the talosconfig, both strictly more powerful
// than an API token.
//
// The config key stays as an override. A cluster stack applied before that
// export existed has nothing to offer, and an operator may deliberately want a
// token scoped differently from the one that built the cluster.
//
// An empty token from either source fails here rather than reaching Hetzner.
// Left alone it becomes a credential that authenticates against nothing, and
// the symptom is a 401 from whatever used it first — a CCM that never clears
// the uninitialized taint, a provider whose load balancer is never created —
// which reads as a broken cluster rather than as a missing credential.
//
// On the runner because two layers need it, and it was in one of them; a
// second copy of a refusal is a refusal that drifts.
func (r *Runner) HcloudToken() pulumi.StringOutput {
	if r.Cfg.Get(HcloudTokenKey) != "" {
		r.Log.Done("token", "from this layer's config, overriding the cluster stack")

		return r.Cfg.RequireSecret(HcloudTokenKey)
	}

	r.Log.Step("token", "from the cluster stack")

	override := r.Ctx.Project() + ":" + HcloudTokenKey

	// Empty rather than absent: internal/pkg/clusterref's version gate has
	// established that the tier publishes this output, and the tier exports it
	// empty when its own `hcloud:token` is unset — a cluster built from an
	// environment variable rather than from stack config. That is a real state
	// with two remedies, not a migration to wait out.
	return pulumix.Cast[pulumi.StringOutput](pulumix.ApplyErr(r.Cluster.HcloudToken,
		func(token string) (string, error) {
			if token == "" {
				return "", fmt.Errorf(
					"the cluster stack exports an empty %q. Either set it there and apply the tier:\n"+
						"  pulumi -C infra/cluster config set --secret hcloud:token <token>\n"+
						"or give this layer its own:\n"+
						"  pulumi config set --secret %s <token>",
					clusterref.OutputHcloudToken, override)
			}

			return token, nil
		}))
}
