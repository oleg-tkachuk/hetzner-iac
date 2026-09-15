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
// in order; applying layers/50-gitops against a cluster with no CNI will simply
// and then fail. That is the trade for being able to touch one layer without
// planning the other four.
package layer

import (
	"fmt"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/clusterref"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/pulumilog"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/pulumiopts"

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
		// Naming the task rather than the pulumi command: the task derives
		// the reference from the cluster tier, where a hand-typed one can
		// name another environment's cluster and nothing rejects it.
		return nil, fmt.Errorf(
			"config `clusterStackRef` is not set: point this layer at the cluster tier with\n" +
				"  task platform:init stack=<stack>")
	}

	cluster, err := clusterref.Resolve(ctx, ref)
	if err != nil {
		return nil, err
	}

	provider, err := kubernetes.NewProvider(ctx, "k8s", &kubernetes.ProviderArgs{
		Kubeconfig: cluster.Kubeconfig,
		// What makes this provider's identity explicit instead of guessed.
		//
		// Without it the provider decides for itself whether a configuration
		// change is an update or a replacement, and it gets that wrong in the
		// expensive direction: this repository has already seen a plan to
		// replace the provider — and with it every release and every secret
		// in every layer — because one output threaded into the kubeconfig
		// changed. With clusterIdentifier set, the provider is replaced only
		// when this value changes, and everything else is an update.
		//
		// The cluster name rather than the endpoint, which also identifies a
		// cluster but is not stable: scaling to three control planes puts an
		// API load balancer in front of the same cluster, and replacing the
		// whole platform for a new address would be the very mistake this
		// setting exists to prevent. The name is this repository's own notion
		// of cluster identity — it prefixes every node and scopes the
		// firewall's label selector.
		ClusterIdentifier: cluster.ClusterName,
		// Server-side apply. It is what makes a re-run converge on a resource
		// another controller also writes to — the field-manager conflict is
		// reported rather than silently overwritten, which is the behaviour
		// that makes these layers safe to apply repeatedly next to Argo CD.
		EnableServerSideApply: pulumi.Bool(true),
		// An unreachable cluster fails the operation instead of dropping
		// every resource from state. The provider's own wording: set to true
		// it "will delete resources associated with an unreachable Kubernetes
		// cluster from Pulumi state" — so on a transient outage the next
		// apply would believe nothing is installed and install it all again.
		//
		// This is the default; it is written out because the opposite reads
		// like a cleanup convenience and is not one. It says nothing about
		// cluster identity — that is `clusterIdentifier`, which this provider
		// does not set.
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

// RunComponents is Run for a layer whose whole body is its component set.
//
// Two layers wrote the same four-line closure around r.Deploy; a layer that
// only deploys its set now says so in one line, and one that does more keeps
// using Run.
func RunComponents(components Components) {
	Run(func(r *Runner) error {
		_, err := r.Deploy(components)

		return err
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
	return pulumiopts.With(r.Options, extra...)
}
