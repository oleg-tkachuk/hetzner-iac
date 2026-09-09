// Package layer is the shim every program under layers/ shares.
//
// A layer is an independent Pulumi project: it can be previewed, applied and
// destroyed on its own, and it knows nothing about the layers beside it. What
// it needs is always the same — the cluster's kubeconfig and a Kubernetes
// provider built from it — so that wiring lives here instead of being copied
// into each main().
//
// Independence has a cost worth naming: ordering between layers is the
// operator's responsibility, not Pulumi's. `task platform:apply` applies them
// in order; applying 40-gitops against a cluster with no CNI will simply wait
// and then fail. That is the trade for being able to touch one layer without
// planning the other four.
package layer

import (
	"fmt"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/clusterref"

	kubernetes "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

// Runner carries everything a layer needs.
type Runner struct {
	Ctx     *pulumi.Context
	Cluster *clusterref.Cluster

	// Provider is authenticated with the cluster's kubeconfig. Every resource
	// a layer creates must be created with it — a resource created against the
	// ambient kubeconfig lands on whatever cluster the operator's shell
	// happened to point at.
	Provider *kubernetes.Provider

	// Options already carry the provider, so layers pass them through rather
	// than remembering to attach it.
	Options []pulumi.ResourceOption
}

// New builds the runner from stack configuration.
func New(ctx *pulumi.Context) (*Runner, error) {
	cfg := config.New(ctx, "")

	ref := cfg.Get("clusterStackRef")
	if ref == "" {
		return nil, fmt.Errorf(
			"config `clusterStackRef` is not set: point this layer at the cluster tier with\n" +
				"  pulumi config set clusterStackRef <org>/hetzner-cluster/<stack>")
	}

	cluster, err := clusterref.Resolve(ctx, ref)
	if err != nil {
		return nil, err
	}

	provider, err := kubernetes.NewProvider(ctx, "k8s", &kubernetes.ProviderArgs{
		Kubeconfig: cluster.Kubeconfig,
		// Server-side apply. It is what makes a re-run converge on a resource
		// another controller also writes to — the field-manager conflict is
		// reported rather than silently overwritten, which is the behaviour
		// that makes these layers safe to apply repeatedly next to Argo CD.
		EnableServerSideApply: pulumi.Bool(true),
		// Refuse to act on a cluster that does not match the kubeconfig this
		// stack resolved. Without it, a stale kubeconfig quietly targets
		// whatever cluster now answers at that address.
		DeleteUnreachable: pulumi.Bool(false),
	})
	if err != nil {
		return nil, fmt.Errorf("kubernetes provider: %w", err)
	}

	return &Runner{
		Ctx:      ctx,
		Cluster:  cluster,
		Provider: provider,
		Options:  []pulumi.ResourceOption{pulumi.Provider(provider)},
	}, nil
}

// Run wraps pulumi.Run with the shared setup and a banner naming the target.
func Run(fn func(*Runner) error) {
	pulumi.Run(func(ctx *pulumi.Context) error {
		runner, err := New(ctx)
		if err != nil {
			return err
		}

		runner.banner()

		return fn(runner)
	})
}

// banner names the layer and the cluster it is about to change.
//
// It goes to the ephemeral (Status) tier: visible live in `pulumi up`, when an
// operator running several layers in one shell needs it, and dropped from the
// final diagnostics so the summary is not the same line five times.
func (r *Runner) banner() {
	r.Cluster.ClusterName.ApplyT(func(name string) string {
		_ = r.Ctx.Log.Info(
			fmt.Sprintf("layer %s → cluster %s (stack %s)", r.Ctx.Project(), name, r.Ctx.Stack()),
			&pulumi.LogArgs{Ephemeral: true})

		return name
	})
}

// With returns the layer's options plus extra ones, without mutating the
// shared slice. Appending to r.Options directly would let one component's
// DependsOn leak into every component created after it.
func (r *Runner) With(extra ...pulumi.ResourceOption) []pulumi.ResourceOption {
	out := make([]pulumi.ResourceOption, 0, len(r.Options)+len(extra))
	out = append(out, r.Options...)
	out = append(out, extra...)

	return out
}
