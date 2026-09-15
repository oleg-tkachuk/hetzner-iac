// Command gitops installs Argo CD.
//
// Argo CD is installed by Pulumi, not by itself. The division is deliberate:
// Pulumi owns the platform — cluster, CNI, cloud integration, ingress,
// observability — and Argo CD owns application workloads. Letting Argo CD
// manage the platform it runs on means a bad sync can remove the thing that
// would fix it.
package main

import (
	"fmt"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/layer"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/platform"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/values"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// AdminSecret is where Argo CD writes its initial admin password. This is the
// NAME of a Kubernetes Secret, not a credential: the password is generated
// in-cluster and deliberately never read by this program.
const AdminSecret = "argocd-initial-admin-secret" // #nosec G101 -- a secret's name, not its value

// StatelessReplicas is how many of each stateless Argo CD component to run,
// so the API, the UI and the repo server survive a node failure. The
// application controller is deliberately not one of them: sharding it needs
// configuration that only pays off with many applications.
const StatelessReplicas = 2

// IssuerName must match the ClusterIssuer created by 30-cluster-services.
const IssuerName = "letsencrypt"

// Stack outputs, named rather than written at the export. See
// TestLayers_ExportOnlyNamedOutputs.
const (
	OutputAdminSecret = "adminSecret"
	OutputReady       = "gitopsReady"
)

// ArgoCDTimeoutSeconds is longer than the default: several images, a Redis and
// five deployments, and the default is tight on a cold cluster.
const ArgoCDTimeoutSeconds = 900

// Components are what this layer deploys. One of them, so the table buys
// ordering nothing needs — what it buys here is the enumeration: layertest
// asserts the chart is pinned and that pkg/workloads knows what it produces.
var Components = layer.Components{
	{
		Chart:          "argo-cd",
		TimeoutSeconds: ArgoCDTimeoutSeconds,
		ValuesYAML: func(r *layer.Runner) (pulumi.AssetOrArchiveArrayInput, error) {
			return values.Asset("argo-cd", r.Cluster.Domain.ApplyT(ArgoCDData)), nil
		},
	},
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

		argocd, ok := deployed.Release("argo-cd")
		if !ok {
			return fmt.Errorf("argo-cd was not deployed")
		}

		// Export the secret's NAME, not its value: reading the password into
		// this stack would put a cluster-admin credential into Pulumi state
		// for no benefit — it is rotated on first login anyway.
		r.Ctx.Export(OutputAdminSecret, pulumi.String(AdminSecret))
		r.Ctx.Export(OutputReady, argocd.Status.Status())

		return nil
	})
}

// ArgoCDData is what pkg/values/argo-cd.yaml.tmpl renders with.
//
// An empty domain installs Argo CD without an Ingress, which is the right
// shape before DNS exists: the UI is then reachable with `kubectl port-forward`
// and nothing is published by accident.
func ArgoCDData(domain string) values.ArgoCD {
	return values.ArgoCD{
		Domain:       domain,
		IngressClass: platform.IngressClass,
		Issuer:       IssuerName,
		Replicas:     StatelessReplicas,
	}
}
