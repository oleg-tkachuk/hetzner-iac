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
	"github.com/oleg-tkachuk/hetzner-iac/pkg/pulumilog"

	kubernetes "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

// Runner carries everything a layer needs.
type Runner struct {
	Ctx     *pulumi.Context
	Cluster *clusterref.Cluster

	// Log writes this layer's output. Every layer shares one vocabulary, so a
	// reader who has seen one layer's output can read the next.
	Log *pulumilog.Logger

	// Cfg is this layer's stack configuration, namespaced to the project.
	//
	// Built with an empty namespace, which Pulumi resolves to the project
	// name. Every layer used to write `config.New(ctx, "gitops")` — its own
	// project name as a literal, in a second place, where renaming a project
	// leaves a layer reading config nobody sets. That is not hypothetical:
	// merging two layers renamed a project and the literal was missed.
	Cfg *config.Config

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

	// Exported so the engine awaits it. The contract check is an output, and
	// an output nothing consumes is never resolved — the error inside it
	// would never surface. A stack output is the cheapest thing that is
	// always awaited and whose value no resource's identity depends on.
	ctx.Export(clusterref.OutputContractVersion, cluster.ContractCheck)

	return &Runner{
		Ctx:      ctx,
		Cluster:  cluster,
		Log:      pulumilog.New(ctx),
		Cfg:      cfg,
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

// banner names the cluster this layer is about to change.
//
// Resolved through ApplyT because the name is an output of another stack: it
// is not known when the program starts, only when the reference resolves.
func (r *Runner) banner() {
	r.Cluster.ClusterName.ApplyT(func(name string) string {
		r.Log.Step("cluster", fmt.Sprintf("stack %s → %s", r.Ctx.Stack(), name))

		return name
	})
}

// StringOr reads a config value, falling back to a default when it is unset.
//
// The pattern it replaces appeared once per tunable, four lines each, and put
// the default a screen away from the key it belongs to:
//
//	retention := cfg.Get("metricsRetention")
//	if retention == "" {
//		retention = DefaultRetention
//	}
func (r *Runner) StringOr(key, fallback string) string {
	if value := r.Cfg.Get(key); value != "" {
		return value
	}

	return fallback
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
