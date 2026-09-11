// Command core installs the platform pieces everything above assumes exist:
// cert-manager, external-secrets and metrics-server.
//
// Deliberately small. Anything only one workload needs belongs with that
// workload; this is the set whose absence breaks something in a way that is
// hard to diagnose — an Ingress with no certificate, a Deployment whose secret
// never materialises, an autoscaler with no metrics.
package main

import (
	"github.com/oleg-tkachuk/hetzner-iac/pkg/chartsettings"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/layer"

	apiextensions "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/apiextensions"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/yaml"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// IssuerName is the ClusterIssuer other layers reference by annotation.
const IssuerName = "letsencrypt"

// LetsEncryptDirectory is the production ACME endpoint. The staging endpoint
// issues untrusted certificates, so using it "to be safe" produces browser
// warnings that look like a misconfiguration.
const LetsEncryptDirectory = "https://acme-v02.api.letsencrypt.org/directory"

// Components are what this layer deploys.
//
// The ClusterIssuer is a component that may decline. Its Create returns
// (nil, nil) when acmeEmail is unset, which keeps the entry in the set — still
// enumerated, still ordered — rather than hiding the decision behind an `if`
// where nothing can see it. A cluster with no public DNS has nothing for
// Let's Encrypt to validate against, and an issuer that fails every order is
// noisier than an absent one.
var Components = layer.Components{
	{
		Chart:  "cert-manager",
		Values: func(*layer.Runner) pulumi.Map { return CertManagerValues() },
	},
	{
		Name:   IssuerName,
		After:  []string{"cert-manager"},
		Create: createClusterIssuer,
	},
	{
		Chart:  "external-secrets",
		Values: func(*layer.Runner) pulumi.Map { return ExternalSecretsValues() },
	},
	{
		// Approves the CSRs the kubelets raise once pkg/hetzner turns on
		// rotate-server-certificates. Nothing in Kubernetes approves them by
		// itself, and until they are approved the kubelet keeps the
		// self-signed certificate that has no IP SANs.
		Name:   CertApproverComponent,
		Create: createCertApprover,
	},
	{
		// After the approver: metrics-server scrapes the kubelet over TLS and
		// verifies the certificate, so it cannot be Ready until the kubelet
		// has one the cluster CA signed. Without this ordering it fails every
		// scrape and Helm waits out its whole timeout — measured at 611s
		// before rolling back.
		Chart:  "metrics-server",
		After:  []string{CertApproverComponent},
		Values: func(*layer.Runner) pulumi.Map { return MetricsServerValues() },
	},
}

// CertApproverComponent is the approver's name in the component set.
const CertApproverComponent = "kubelet-serving-cert-approver"

// CertApproverManifest is the vendored manifest, pinned by content.
const CertApproverManifest = "manifests/kubelet-serving-cert-approver.yaml"

// createCertApprover applies the vendored approver manifest.
//
// A manifest rather than a chart because upstream publishes no chart — only
// kustomize bases and two flat installs. ConfigFile reads the committed copy,
// so there is no fetch at apply time and the diff is reviewable.
func createCertApprover(r *layer.Runner, dependencies []pulumi.Resource) (pulumi.Resource, error) {
	return yaml.NewConfigFile(r.Ctx, CertApproverComponent, &yaml.ConfigFileArgs{
		File: CertApproverManifest,
	}, r.With(pulumi.DependsOn(dependencies))...)
}

// createClusterIssuer makes the ACME issuer, or nothing and says so.
func createClusterIssuer(r *layer.Runner, dependencies []pulumi.Resource) (pulumi.Resource, error) {
	email := r.Cfg.Get("acmeEmail")
	if email == "" {
		// The most confusing thing this layer can do is install cert-manager
		// and no issuer, leaving every Certificate pending with nothing to
		// satisfy it. Permanent, so it survives the run.
		r.Log.Skipped("cluster-issuer", "acmeEmail unset, no ClusterIssuer created")

		return nil, nil
	}

	r.Log.Step("cluster-issuer", "acmeEmail set, orders go to Let's Encrypt")

	// An untyped CustomResource because the CRD is installed by cert-manager,
	// which this component follows: a generated, typed SDK would have to come
	// from CRDs that do not exist at compile time.
	return apiextensions.NewCustomResource(r.Ctx, IssuerName, &apiextensions.CustomResourceArgs{
		ApiVersion: pulumi.String("cert-manager.io/v1"),
		Kind:       pulumi.String("ClusterIssuer"),
		Metadata:   &metav1.ObjectMetaArgs{Name: pulumi.String(IssuerName)},
		OtherFields: map[string]any{
			"spec": IssuerSpec(email),
		},
	}, r.With(pulumi.DependsOn(dependencies))...)
}

func main() {
	layer.Run(func(r *layer.Runner) error {
		_, err := r.Deploy(Components)

		return err
	})
}

// CertManagerValues builds the cert-manager values.
func CertManagerValues() pulumi.Map {
	return pulumi.Map{
		"crds": pulumi.Map{
			// CRDs come with the release. Managing them separately is right
			// when several things install cert-manager; this cluster has one.
			"enabled": pulumi.Bool(true),
			// Leave them behind on uninstall. Removing the CRDs deletes every
			// Certificate and Issuer in the cluster — a far larger action than
			// uninstalling a chart, and not one an uninstall should imply.
			"keep": pulumi.Bool(true),
		},
		"prometheus": pulumi.Map{
			"enabled": pulumi.Bool(true),
			// Owned by 60-observability, which installs the operator CRDs.
			"servicemonitor": pulumi.Map{"enabled": pulumi.Bool(false)},
		},
	}
}

// ExternalSecretsValues builds the external-secrets values.
func ExternalSecretsValues() pulumi.Map {
	return pulumi.Map{
		"installCRDs":    pulumi.Bool(true),
		"webhook":        pulumi.Map{"create": pulumi.Bool(true)},
		"serviceMonitor": pulumi.Map{"enabled": pulumi.Bool(false)},
	}
}

// MetricsServerValues builds the metrics-server values.
func MetricsServerValues() pulumi.Map {
	return pulumi.Map{
		"args": pulumi.ToStringArray([]string{
			// Talos nodes are addressed on the private network and their
			// kubelet certificates carry the internal address. The chart's
			// default preference order tries the hostname first, which does
			// not resolve here — so metrics-server starts and every scrape
			// fails, and the autoscaler is silently blind.
			chartsettings.MetricsServerAddressTypes,
		}),
		// Two replicas so a node failure does not take metrics — and with them
		// the horizontal pod autoscaler — down.
		"replicas": pulumi.Int(2),
		"podDisruptionBudget": pulumi.Map{
			"enabled":      pulumi.Bool(true),
			"minAvailable": pulumi.Int(1),
		},
	}
}

// IssuerSpec builds the ACME ClusterIssuer spec.
//
// HTTP-01 through the nginx ingress class, which means the issuer only works
// once 40-ingress is applied. DNS-01 would remove that ordering but needs
// provider credentials this layer deliberately does not hold.
func IssuerSpec(email string) pulumi.Map {
	return pulumi.Map{
		"acme": pulumi.Map{
			"server": pulumi.String(LetsEncryptDirectory),
			"email":  pulumi.String(email),
			"privateKeySecretRef": pulumi.Map{
				"name": pulumi.String("letsencrypt-account-key"),
			},
			"solvers": pulumi.Array{
				pulumi.Map{
					"http01": pulumi.Map{
						"ingress": pulumi.Map{
							"ingressClassName": pulumi.String("nginx"),
						},
					},
				},
			},
		},
	}
}
