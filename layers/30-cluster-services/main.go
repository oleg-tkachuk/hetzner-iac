// Command cluster-services installs the services the rest of the cluster
// consumes: cert-manager, external-secrets and metrics-server, plus the
// ClusterIssuer and the kubelet-serving-certificate approver.
//
// Named for what they are rather than for their importance. None of them is
// needed to make a node Ready — that is 10-node-platform, which holds the CNI
// and the cloud controller manager, and which this layer would be a worse
// name for. What these have in common is that something else asks them for
// something: a certificate, a secret, a metric.
//
// Deliberately small. Anything only one workload needs belongs with that
// workload; this is the set whose absence breaks something in a way that is
// hard to diagnose — an Ingress with no certificate, a Deployment whose secret
// never materialises, an autoscaler with no metrics.
package main

import (
	"github.com/oleg-tkachuk/hetzner-iac/pkg/chartsettings"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/layer"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/values"

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

// MetricsServerReplicas is how many metrics-server pods to run.
const MetricsServerReplicas = 2

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
		Chart:      "cert-manager",
		ValuesYAML: static("cert-manager", nil),
	},
	{
		Name:   IssuerName,
		After:  []string{"cert-manager"},
		Create: createClusterIssuer,
	},
	{
		Chart:      "external-secrets",
		ValuesYAML: static("external-secrets", nil),
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
		Chart:      "metrics-server",
		After:      []string{CertApproverComponent},
		ValuesYAML: static("metrics-server", MetricsServerData()),
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
	}, r.With(layer.DependsOn(dependencies)...)...)
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
	}, r.With(layer.DependsOn(dependencies)...)...)
}

func main() {
	layer.Run(func(r *layer.Runner) error {
		_, err := r.Deploy(Components)

		return err
	})
}

// MetricsServerData is the data the metrics-server template renders with.
func MetricsServerData() values.MetricsServer {
	return values.MetricsServer{
		AddressTypes: chartsettings.MetricsServerAddressTypes,
		Replicas:     MetricsServerReplicas,
	}
}

// static renders a template whose values need nothing resolved.
//
// It used to log the error and return nil, which installed the chart on its
// own defaults — the outcome these templates exist to prevent. The error now
// stops the run.
func static(chart string, data any) func(*layer.Runner) (pulumi.AssetOrArchiveArrayInput, error) {
	return func(*layer.Runner) (pulumi.AssetOrArchiveArrayInput, error) {
		return values.Static(chart, data)
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
