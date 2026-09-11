// Command gitops installs Argo CD.
//
// Argo CD is installed by Pulumi, not by itself. The division is deliberate:
// Pulumi owns the platform — cluster, CNI, cloud integration, ingress,
// observability — and Argo CD owns application workloads. Letting Argo CD
// manage the platform it runs on means a bad sync can remove the thing that
// would fix it.
package main

import (
	"github.com/oleg-tkachuk/hetzner-iac/pkg/layer"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// AdminSecret is where Argo CD writes its initial admin password. This is the
// NAME of a Kubernetes Secret, not a credential: the password is generated
// in-cluster and deliberately never read by this program.
const AdminSecret = "argocd-initial-admin-secret" // #nosec G101 -- a secret's name, not its value

// IssuerName must match the ClusterIssuer created by 30-core.
const IssuerName = "letsencrypt"

func main() {
	layer.Run(func(r *layer.Runner) error {
		domain := r.Cfg.Get("domain")
		if domain == "" {
			r.Log.Skipped("ingress", "domain unset, reach the UI with kubectl port-forward")
		} else {
			r.Log.Step("ingress", "domain "+domain)
		}

		release, err := r.Release(r.Ctx, layer.ReleaseArgs{
			Chart: "argo-cd",
			// Several images, a Redis and five deployments; the default
			// timeout is tight on a cold cluster.
			TimeoutSeconds: 900,
			Values:         ArgoCDValues(domain),
		})
		if err != nil {
			return err
		}

		// Export the secret's NAME, not its value: reading the password into
		// this stack would put a cluster-admin credential into Pulumi state
		// for no benefit — it is rotated on first login anyway.
		r.Ctx.Export("adminSecret", pulumi.String(AdminSecret))
		r.Ctx.Export("gitopsReady", release.Status.Status())

		return nil
	})
}

// ArgoCDValues builds the Argo CD values.
//
// An empty domain installs Argo CD without an Ingress, which is the right
// shape before DNS exists: the UI is then reachable with `kubectl port-forward`
// and nothing is published by accident.
func ArgoCDValues(domain string) pulumi.Map {
	server := pulumi.Map{
		// Two replicas: the API and UI should survive a node failure.
		"replicas": pulumi.Int(2),
		// TLS terminates at the ingress, so the API server speaks plaintext
		// behind it. Running TLS on both sides produces a redirect loop that
		// is tedious to diagnose.
		"extraArgs": pulumi.ToStringArray([]string{"--insecure"}),
		"metrics": pulumi.Map{
			"enabled":        pulumi.Bool(true),
			"serviceMonitor": pulumi.Map{"enabled": pulumi.Bool(false)},
		},
	}

	if domain != "" {
		server["ingress"] = pulumi.Map{
			"enabled":          pulumi.Bool(true),
			"ingressClassName": pulumi.String("nginx"),
			"hostname":         pulumi.String(domain),
			"annotations": pulumi.Map{
				"cert-manager.io/cluster-issuer": pulumi.String(IssuerName),
			},
			"tls": pulumi.Bool(true),
		}
	}

	return pulumi.Map{
		"global": pulumi.Map{"domain": pulumi.String(domain)},
		// The application controller is the component that actually
		// reconciles; it is deliberately single-replica because sharding it
		// needs configuration that only pays off with many applications.
		"controller": pulumi.Map{
			"replicas": pulumi.Int(1),
			"metrics": pulumi.Map{
				"enabled":        pulumi.Bool(true),
				"serviceMonitor": pulumi.Map{"enabled": pulumi.Bool(false)},
			},
		},
		"repoServer": pulumi.Map{
			"replicas": pulumi.Int(2),
			"metrics": pulumi.Map{
				"enabled":        pulumi.Bool(true),
				"serviceMonitor": pulumi.Map{"enabled": pulumi.Bool(false)},
			},
		},
		"server":         server,
		"applicationSet": pulumi.Map{"replicas": pulumi.Int(2)},
		"redis-ha":       pulumi.Map{"enabled": pulumi.Bool(false)},
		"configs": pulumi.Map{
			"params": pulumi.Map{"server.insecure": pulumi.Bool(true)},
		},
	}
}
