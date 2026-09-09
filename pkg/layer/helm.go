package layer

import (
	"fmt"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/charts"

	helm "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/helm/v3"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// Default lifecycle settings for every release this platform installs.
const (
	// defaultTimeoutSeconds covers a chart that pulls large images onto cold
	// nodes. Charts that legitimately take longer override it.
	defaultTimeoutSeconds = 600

	// maxHistory caps the Helm release history kept in the cluster. Helm's
	// default is unbounded, and on a platform reconciled repeatedly that grows
	// into thousands of secrets in kube-system.
	maxHistory = 5
)

// ReleaseArgs is the per-release detail a layer supplies.
type ReleaseArgs struct {
	// Chart is a key into the pinned registry, never a chart name — the
	// version comes with it, so a layer cannot install an unpinned chart.
	Chart string

	// Name overrides the Helm release name. Empty uses the registry key,
	// which is what makes `helm list -A` read like the layer list.
	Name string

	// Namespace overrides the registry's namespace. Rarely needed.
	Namespace string

	// Values are the chart values. They are Pulumi inputs, so an output from
	// another resource — a generated password, the pod CIDR from the cluster
	// tier — can be threaded in without resolving it first.
	Values pulumi.Map

	// TimeoutSeconds overrides the default for a chart that is genuinely slow.
	TimeoutSeconds int

	// SkipCRDs leaves custom resource definitions alone. Needed when CRDs are
	// managed separately, which is the usual answer for charts whose CRDs
	// outlive the release.
	SkipCRDs bool
}

// Release installs one pinned Helm chart.
//
// Why a real Helm release rather than the newer Chart resource, which renders
// manifests into the Pulumi graph and would give per-object diffs and
// CrossGuard coverage: Chart renders with `helm template`, which does not run
// Helm hooks. Several charts here depend on them — ingress-nginx generates its
// admission-webhook certificate in a hook Job, kube-prometheus-stack does the
// same for its webhook, cert-manager runs startupapicheck — and a chart whose
// hooks never run installs cleanly and then misbehaves. Correct installation
// wins over diff granularity.
//
// Atomic and WaitForJobs together are what make the layer idempotent in the
// way that matters operationally: a failed upgrade rolls back instead of
// leaving half a release behind, so re-running converges rather than
// compounding.
func (r *Runner) Release(ctx *pulumi.Context, args ReleaseArgs, opts ...pulumi.ResourceOption) (*helm.Release, error) {
	chart, err := charts.Get(args.Chart)
	if err != nil {
		return nil, err
	}

	name := args.Name
	if name == "" {
		name = args.Chart
	}

	namespace := args.Namespace
	if namespace == "" {
		namespace = chart.Namespace
	}

	timeout := args.TimeoutSeconds
	if timeout == 0 {
		timeout = defaultTimeoutSeconds
	}

	values := args.Values
	if values == nil {
		values = pulumi.Map{}
	}

	release, err := helm.NewRelease(ctx, name, &helm.ReleaseArgs{
		Name:            pulumi.String(name),
		Chart:           pulumi.String(chart.Name),
		Version:         pulumi.String(chart.Version),
		Namespace:       pulumi.String(namespace),
		CreateNamespace: pulumi.Bool(true),
		RepositoryOpts: &helm.RepositoryOptsArgs{
			Repo: pulumi.String(chart.Repo),
		},
		Values: values,
		// Roll back a failed upgrade rather than leaving a half-applied
		// release for the next run to inherit.
		Atomic: pulumi.Bool(true),
		// Hook Jobs must finish before the release is called successful;
		// without this a chart reports ready while its webhook still has no
		// certificate.
		WaitForJobs: pulumi.Bool(true),
		Timeout:     pulumi.Int(timeout),
		MaxHistory:  pulumi.Int(maxHistory),
		SkipCrds:    pulumi.Bool(args.SkipCRDs),
	}, r.With(opts...)...)
	if err != nil {
		return nil, fmt.Errorf("helm release %q (chart %s %s): %w", name, chart.Name, chart.Version, err)
	}

	return release, nil
}
