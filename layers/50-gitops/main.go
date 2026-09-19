// Command gitops installs Argo CD.
//
// Argo CD is installed by Pulumi, not by itself. The division is deliberate:
// Pulumi owns the platform — cluster, CNI, cloud integration, ingress,
// observability — and Argo CD owns application workloads. Letting Argo CD
// manage the platform it runs on means a bad sync can remove the thing that
// would fix it.
package main

import (
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/charts"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/layer"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/values"

	"github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/apiextensions"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// Chart is the registry key, which is also what the values template is named
// after and the name the component is known by in the set. From the chart's
// own declaration rather than spelled again here.
const Chart = charts.ArgoCD

// AdminSecret is where Argo CD writes its initial admin password. This is the
// NAME of a Kubernetes Secret, not a credential: the password is generated
// in-cluster and deliberately never read by this program.
const AdminSecret = "argocd-initial-admin-secret" // #nosec G101 -- a secret's name, not its value

// StatelessReplicas is how many of each stateless Argo CD component to run,
// so the API, the UI and the repo server survive a node failure. The
// application controller is deliberately not one of them: sharding it needs
// configuration that only pays off with many applications.
const StatelessReplicas = 2

// Stack outputs, named rather than written at the export. See
// TestLayers_ExportOnlyNamedOutputs.
const (
	OutputAdminSecret = "adminSecret"
	OutputReady       = "gitopsReady"
)

// ArgoCDTimeoutSeconds is longer than the default: several images, a Redis and
// five deployments, and the default is tight on a cold cluster.
const ArgoCDTimeoutSeconds = 900

// Namespace is where Argo CD is installed, taken from the chart registry
// rather than written twice: the root Application and its project have to land
// in the namespace the chart actually used.
var Namespace = charts.MustGet(Chart).Namespace

// The root Application and the project that governs it. One name each, and
// they are what an operator sees in the UI.
const (
	RootApplication = "root"
	RootProject     = "root"
)

// Config keys this layer reads for the root Application. Pulumi.yaml declares
// each with `default: ""`, so the Go constants below stay the one place a
// fallback is written.
const (
	RepoURLKey  = "repoURL"
	PathKey     = "path"
	RevisionKey = "revision"
)

// DefaultRootPath is the repository root, where a tree of Applications
// usually starts.
const DefaultRootPath = "."

// DefaultRootRevision tracks the repository's default branch. Named rather
// than pinned: a repository calling it `main` and one calling it `develop`
// both work, and pinning a branch here would be a decision about somebody
// else's repository.
const DefaultRootRevision = "HEAD"

// ArgoAPIVersion is the API both objects below belong to.
const ArgoAPIVersion = "argoproj.io/v1alpha1"

// APIServer is the in-cluster address Argo CD deploys to. The root never
// targets another cluster: this layer installs Argo CD into the cluster it is
// applied to.
const APIServer = "https://kubernetes.default.svc"

// Finalizer makes deleting the root Application delete what it created.
//
// Without it `pulumi destroy` removes the Application and leaves every child
// Application — and every workload under them — running, owned by nothing.
// With it the destroy cascades, which is the property every other layer here
// already has.
const Finalizer = "resources-finalizer.argocd.argoproj.io"

// Components are what this layer deploys. One of them, so the table buys
// ordering nothing needs — what it buys here is the enumeration: layertest
// asserts the chart is pinned and that internal/pkg/workloads knows what it produces.
var Components = layer.Components{
	{
		Chart:          Chart,
		TimeoutSeconds: ArgoCDTimeoutSeconds,
		ValuesFrom: func(r *layer.Runner) pulumi.Output {
			return r.Cluster.Domain.ApplyT(ArgoCDData)
		},
	},
	{
		Name:   RepositoryCredential,
		After:  []string{Chart},
		Create: createRepoCredential,
	},
	{
		Name: RootApplication,
		// After the credential as well as the chart: an Application that
		// syncs before its Secret exists reports an authentication error and
		// is retried, which is recoverable and reads like a broken
		// repository. A component that declined is simply absent from the
		// set, so this is not a dependency on a credential being configured.
		After:  []string{Chart, RepositoryCredential},
		Create: createRoot,
	},
}

// createRoot points Argo CD at the repository that holds the workloads, or at
// nothing and says so.
//
// This layer ships the mechanism, not the content. WHICH repository holds the
// workloads is a deployment decision rather than a property of this
// repository, so it arrives as config — exactly the way acmeEmail and
// metadata.domain do — and an unset repoURL leaves Argo CD installed and
// reconciling nothing.
//
// The project is created here rather than as a component of its own because
// the two are one decision. A root Application with no project either runs
// under `default`, which permits everything everywhere, or names a project
// that does not exist and never syncs.
func createRoot(r *layer.Runner, dependencies []pulumi.Resource) (pulumi.Resource, error) {
	repoURL := r.Cfg.Get(RepoURLKey)
	if repoURL == "" {
		// Permanent, so it survives the run: an Argo CD that reconciles
		// nothing looks exactly like one that is broken.
		r.Log.Skipped(RootApplication, RepoURLKey+" unset, Argo CD reconciles nothing")

		return nil, nil
	}

	path := r.StringOr(PathKey, DefaultRootPath)
	revision := r.StringOr(RevisionKey, DefaultRootRevision)

	r.Log.Step(RootApplication, repoURL+" "+revision+" "+path)

	project, err := apiextensions.NewCustomResource(r.Ctx, RootProject, &apiextensions.CustomResourceArgs{
		ApiVersion:  pulumi.String(ArgoAPIVersion),
		Kind:        pulumi.String("AppProject"),
		Metadata:    &metav1.ObjectMetaArgs{Name: pulumi.String(RootProject), Namespace: pulumi.String(Namespace)},
		OtherFields: map[string]any{"spec": RootProjectSpec(repoURL)},
	}, r.With(layer.DependsOn(dependencies)...)...)
	if err != nil {
		return nil, err
	}

	// After the project: an Application naming a project that does not exist
	// is rejected, and Argo CD does not retry the rejection.
	return apiextensions.NewCustomResource(r.Ctx, RootApplication, &apiextensions.CustomResourceArgs{
		ApiVersion: pulumi.String(ArgoAPIVersion),
		Kind:       pulumi.String("Application"),
		Metadata: &metav1.ObjectMetaArgs{
			Name:       pulumi.String(RootApplication),
			Namespace:  pulumi.String(Namespace),
			Finalizers: pulumi.StringArray{pulumi.String(Finalizer)},
		},
		OtherFields: map[string]any{"spec": RootApplicationSpec(repoURL, path, revision)},
	}, r.With(layer.DependsOn(append(dependencies, project))...)...)
}

// RootProjectSpec governs what the root Application may create.
//
// Argo CD objects only, in Argo CD's own namespace, from the one repository
// the root was pointed at. That is a root's whole job: it creates more
// Applications, and each of those carries its own project deciding what THAT
// one may create. A root permitted to create arbitrary cluster resources
// would make every child project decorative.
func RootProjectSpec(repoURL string) map[string]any {
	return map[string]any{
		"description": "The root Application and nothing else. Children carry their own projects.",
		"sourceRepos": []string{repoURL},
		"destinations": []map[string]any{
			{"server": APIServer, "namespace": Namespace},
		},
		// An empty list is a deny, not an absence: Argo CD reads a MISSING
		// whitelist as "everything", so the empty one has to be written.
		"clusterResourceWhitelist": []map[string]any{},
		"namespaceResourceWhitelist": []map[string]any{
			{"group": "argoproj.io", "kind": "Application"},
			{"group": "argoproj.io", "kind": "ApplicationSet"},
			{"group": "argoproj.io", "kind": "AppProject"},
		},
	}
}

// RootApplicationSpec is the root itself.
//
// Automated, with prune and self-heal, because a root that has to be synced by
// hand is a root nobody trusts: the point of pointing Argo CD at a repository
// is that the repository wins. Prune included — a child Application deleted
// from git has to leave the cluster, or the tree keeps things no commit
// explains.
func RootApplicationSpec(repoURL, path, revision string) map[string]any {
	return map[string]any{
		"project": RootProject,
		"source": map[string]any{
			"repoURL":        repoURL,
			"path":           path,
			"targetRevision": revision,
		},
		"destination": map[string]any{
			"server":    APIServer,
			"namespace": Namespace,
		},
		"syncPolicy": map[string]any{
			"automated": map[string]any{"prune": true, "selfHeal": true},
			// ServerSideApply, because a tree of Applications is exactly the
			// case that hits the client-side apply annotation size limit.
			"syncOptions": []string{"ServerSideApply=true"},
		},
	}
}

func main() {
	layer.Run(func(r *layer.Runner) error {
		// The domain comes from the cluster tier, not from this layer's
		// config. Two layers must spell it identically — 40-ingress points DNS
		// records at its load balancer and this one gives Argo CD a hostname —
		// and a value each stack held its own copy of would drift silently: an
		// Ingress for one name behind a record for another is accepted by
		// everything and serves nothing.
		//
		// Inside an apply, so the line appears with what it explains.
		r.Cluster.Domain.ApplyT(func(domain string) string {
			if domain == "" {
				r.Log.Skipped("ingress", "metadata.domain unset, reach the UI with kubectl port-forward")
			} else {
				r.Log.Step("ingress", "metadata.domain "+domain)
			}

			return domain
		})

		deployed, err := r.Deploy(Components)
		if err != nil {
			return err
		}

		argocd, err := deployed.MustRelease(Chart)
		if err != nil {
			return err
		}

		// Export the secret's NAME, not its value: reading the password into
		// this stack would put a cluster-admin credential into Pulumi state
		// for no benefit — it is rotated on first login anyway.
		r.Ctx.Export(OutputAdminSecret, pulumi.String(AdminSecret))
		r.Ctx.Export(OutputReady, argocd.Status.Status())

		return nil
	})
}

// ArgoCDData is what internal/pkg/values/argo-cd.yaml.tmpl renders with.
//
// An empty domain installs Argo CD without an Ingress, which is the right
// shape before DNS exists: the UI is then reachable with `kubectl port-forward`
// and nothing is published by accident.
func ArgoCDData(domain string) values.ArgoCD {
	return values.ArgoCD{
		Domain:       domain,
		IngressClass: platform.IngressClass,
		Issuer:       platform.IssuerName,
		Replicas:     StatelessReplicas,
	}
}
