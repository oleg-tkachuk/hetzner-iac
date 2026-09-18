// Command cluster-services installs the services the rest of the cluster
// consumes: cert-manager, external-secrets and metrics-server, plus the
// ClusterIssuer, the kubelet-serving-certificate approver, and KEDA when it is
// asked for.
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
	"fmt"
	"strconv"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/chartsettings"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/layer"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/values"

	apiextensions "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/apiextensions"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/yaml"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// The two ACME endpoints, and the account key each registers against.
//
// Production is the default: staging issues untrusted certificates, so using
// it "to be safe" produces browser warnings that look like a
// misconfiguration.
//
// Staging exists for one reason, and it is a real one. Let's Encrypt
// rate-limits certificates per registered domain, and a misconfigured Ingress
// burns those attempts — an HTTP-01 order that cannot be validated because
// DNS points somewhere else, or because the ingress class is wrong, fails and
// counts. The staging endpoint's limits are far higher, so the first attempt
// at a new domain belongs there.
//
// The account key is named per endpoint, which is not cosmetic: an ACME
// account is registered with one directory, and a key registered against
// staging is not an account at production. Sharing one secret between them
// leaves cert-manager re-registering on every switch, and the failure reads
// as an authorization problem rather than as the wrong key.
const (
	LetsEncryptProduction = "https://acme-v02.api.letsencrypt.org/directory"
	LetsEncryptStaging    = "https://acme-staging-v02.api.letsencrypt.org/directory"

	accountKeyProduction = "letsencrypt-account-key"
	accountKeyStaging    = "letsencrypt-staging-account-key"
)

// MetricsServerReplicas is how many metrics-server pods to run.
const MetricsServerReplicas = 2

// KedaChart is KEDA's key in internal/pkg/charts.
const KedaChart = "keda"

// KedaEnabledKey is the stack config switch that installs it. Spelled once,
// here: the layer reads it and Pulumi.yaml declares it.
const KedaEnabledKey = "kedaEnabled"

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
		Chart: "cert-manager",
	},
	{
		Name:   platform.IssuerName,
		After:  []string{"cert-manager"},
		Create: createClusterIssuer,
	},
	{
		Chart: "external-secrets",
	},
	{
		// Approves the CSRs the kubelets raise once internal/pkg/clusterspec turns on
		// rotate-server-certificates. Nothing in Kubernetes approves them by
		// itself, and until they are approved the kubelet keeps the
		// self-signed certificate that has no IP SANs.
		Name:   CertApproverComponent,
		Create: createCertApprover,
	},
	{
		// Event-driven autoscaling, and the only optional chart here.
		//
		// No After, and each half of that is worth stating. Not cert-manager:
		// KEDA signs its own webhook and metrics-server certificates and
		// patches the APIService's CA bundle itself, so the cert-manager
		// integration would be a dependency bought for nothing. Not
		// metrics-server either: KEDA serves external.metrics.k8s.io, which is
		// a different API group from the resource metrics metrics-server
		// serves, and neither needs the other to start.
		//
		// What it cannot do is add nodes. The worker pools are pinned in the
		// committed topology, so scaling past their capacity leaves pods
		// Pending — the value here is scale-to-zero and bursts inside the
		// capacity that is already paid for.
		Chart: KedaChart,
		When:  kedaRequested,
	},
	{
		// After the approver: metrics-server scrapes the kubelet over TLS and
		// verifies the certificate, so it cannot be Ready until the kubelet
		// has one the cluster CA signed. Without this ordering it fails every
		// scrape and Helm waits out its whole timeout — measured at 611s
		// before rolling back.
		Chart:        "metrics-server",
		After:        []string{CertApproverComponent},
		StaticValues: MetricsServerData(),
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

	staging := r.Cfg.GetBool("acmeStaging")

	endpoint := "production"
	if staging {
		endpoint = "staging — certificates will not be trusted by a browser"
	}

	r.Log.Step("cluster-issuer", "acmeEmail set, orders go to Let's Encrypt "+endpoint)

	// An untyped CustomResource because the CRD is installed by cert-manager,
	// which this component follows: a generated, typed SDK would have to come
	// from CRDs that do not exist at compile time.
	return apiextensions.NewCustomResource(r.Ctx, platform.IssuerName, &apiextensions.CustomResourceArgs{
		ApiVersion: pulumi.String("cert-manager.io/v1"),
		Kind:       pulumi.String("ClusterIssuer"),
		Metadata:   &metav1.ObjectMetaArgs{Name: pulumi.String(platform.IssuerName)},
		OtherFields: map[string]any{
			"spec": IssuerSpec(email, staging),
		},
	}, r.With(layer.DependsOn(dependencies)...)...)
}

func main() {
	layer.RunComponents(Components)
}

// kedaRequested answers whether this cluster wants KEDA, and says so when it
// does not.
//
// Permanent, so it survives the run: a cluster with cert-manager and no
// ClusterIssuer is confusing in the same way as one whose ScaledObjects are
// accepted by the API server — they are just CRs — and then scale nothing,
// because the controller reading them was never installed.
func kedaRequested(r *layer.Runner) (bool, error) {
	enabled, err := parseEnabled(KedaEnabledKey, r.Cfg.Get(KedaEnabledKey))
	if err != nil {
		return false, err
	}

	if !enabled {
		r.Log.Skipped(KedaChart, KedaEnabledKey+" is not set, so nothing here scales on events")
	}

	return enabled, nil
}

// parseEnabled reads a switch, and refuses a value that is not one.
//
// An unparseable value is an error rather than a silent false, which is the
// argument layers/20-network-policy makes about its own switch and it holds
// here for the same reason: the two states do not look different from outside.
// A cluster that was never asked for KEDA and a cluster where `kedaEnabled:
// yes` was read as false both have no autoscaler, and the second one has an
// operator who believes otherwise.
func parseEnabled(key, value string) (bool, error) {
	if value == "" {
		return false, nil
	}

	enabled, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf(
			"config %q is %q, which is not a boolean: set it to true or false", key, value)
	}

	return enabled, nil
}

// MetricsServerData is the data the metrics-server template renders with.
func MetricsServerData() values.MetricsServer {
	return values.MetricsServer{
		AddressTypes: chartsettings.MetricsServerAddressTypes,
		Replicas:     MetricsServerReplicas,
	}
}

// IssuerSpec builds the ACME ClusterIssuer spec.
//
// HTTP-01 through the class 40-ingress registers, which means the issuer only
// works once that layer is applied. DNS-01 would remove the ordering but needs
// provider credentials this layer deliberately does not hold.
//
// The class comes from internal/pkg/platform rather than a literal here, and that is
// the whole lesson of this function: it WAS the literal "nginx", left behind
// when Traefik replaced ingress-nginx. cert-manager would have created an
// Ingress for the challenge, no controller would have owned it, Let's Encrypt
// would never have reached /.well-known/acme-challenge/, and the order would
// have sat pending for ever with nothing reporting an error. The same literal
// had already been fixed once, in the gitops layer — internal/pkg/platform exists
// because of it — and this copy survived in a second place.
func IssuerSpec(email string, staging bool) map[string]any {
	directory, accountKey := LetsEncryptProduction, accountKeyProduction
	if staging {
		directory, accountKey = LetsEncryptStaging, accountKeyStaging
	}

	// map[string]any, which is what OtherFields takes, and the same spelling
	// 50-gitops uses for its two specs. It was pulumi.Map and pulumi.String
	// throughout, wrapping values that are all static — an email, a URL, a
	// class name — in Input types nothing ever resolved. Two spellings for one
	// job, and the wrapping had to be read past to see there was no Output in
	// here at all.
	return map[string]any{
		"acme": map[string]any{
			"server": directory,
			"email":  email,
			"privateKeySecretRef": map[string]any{
				"name": accountKey,
			},
			"solvers": []map[string]any{
				{
					"http01": map[string]any{
						"ingress": map[string]any{
							"ingressClassName": platform.IngressClass,
						},
					},
				},
			},
		},
	}
}
