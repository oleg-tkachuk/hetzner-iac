// Command cluster-services installs the services the rest of the cluster
// consumes, and the ingress that traffic reaches it through.
//
// TWO GROUPS IN ONE PROJECT, and that is the experiment this layer is. They
// were layers/30-cluster-services and 40-ingress: separate Pulumi
// projects, separate stacks, applied in the order a `for` loop in a taskfile
// happened to iterate. Merged, the order is a dependency graph the engine
// holds, and `platform:init` has one stack fewer to configure.
//
// What keeps them distinguishable is layer.Group: each is a component resource,
// so its type is in every child's URN and either can be addressed on its own —
// `pulumi up --target '**:Ingress$**'`. What keeps them independent is that
// nothing in one follows anything in the other, which order() refuses rather
// than trusts.
//
// The cluster services are named for what they are rather than for their
// importance. None of them is needed to make a node Ready — that is
// 10-node-platform, which holds the CNI and the cloud controller manager. What
// they have in common is that something else asks them for something: a
// certificate, a secret, a metric.
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
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/hetzner"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/layer"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/values"

	"github.com/pulumi/pulumi-hcloud/sdk/go/hcloud"
	apiextensions "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/apiextensions"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/yaml"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumix"
)

// The two groups this project holds.
//
// The TYPE is what matters and it is distinct per group: it is what appears in
// every child's URN, so it is what `--target` selects on and what tells a
// reader of `pulumi stack --show-urns` which former layer a release came from.
// A shared type would leave both sets indistinguishable in the state, which is
// the thing the merge must not cost.
//
// Named after the layers they were, so a URN read six months from now still
// maps onto the directory somebody remembers.
var (
	ClusterServices = layer.Group{
		Type: "hetzner-iac:platform:ClusterServices",
		Name: "cluster-services",
	}

	Ingress = layer.Group{
		Type: "hetzner-iac:platform:Ingress",
		Name: "ingress",
	}
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

// components are what this project deploys, in two groups.
//
// A function rather than a var because one entry needs the Hetzner provider,
// which exists only once the cluster tier's token has been read. The provider
// is created once in deploy and closed over, rather than per component: two
// providers would be two resources in state for one credential.
//
// Every entry carries a Group. That is the merge's whole surface in this file:
// one field per component saying which former layer it is, and no cross-group
// After anywhere — which order() enforces rather than leaves to review.
//
// The ClusterIssuer is a component that may decline. Its Create returns
// (nil, nil) when acmeEmail is unset, which keeps the entry in the set — still
// enumerated, still ordered — rather than hiding the decision behind an `if`
// where nothing can see it. A cluster with no public DNS has nothing for
// Let's Encrypt to validate against, and an issuer that fails every order is
// noisier than an absent one.
func components(provider pulumi.ProviderResource) layer.Components {
	return layer.Components{
		{
			Group: ClusterServices,
			Chart: "cert-manager",
		},
		{
			Group:  ClusterServices,
			Name:   platform.IssuerName,
			After:  []string{"cert-manager"},
			Create: createClusterIssuer,
		},
		{
			Group: ClusterServices,
			Chart: "external-secrets",
		},
		{
			Group: ClusterServices,
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
			Group: ClusterServices,
			Chart: KedaChart,
			When:  kedaRequested,
		},
		{
			Group: ClusterServices,
			// After the approver: metrics-server scrapes the kubelet over TLS and
			// verifies the certificate, so it cannot be Ready until the kubelet
			// has one the cluster CA signed. Without this ordering it fails every
			// scrape and Helm waits out its whole timeout — measured at 611s
			// before rolling back.
			Chart:        "metrics-server",
			After:        []string{CertApproverComponent},
			StaticValues: MetricsServerData(),
		},

		// ── Ingress ─────────────────────────────────────────────────────────
		//
		// Traefik and the load balancer in front of it. Nothing here follows
		// anything above, and that is not an omission: cert-manager issues
		// certificates for an Ingress by watching it at RUN time, so Traefik
		// installs against a cluster that has no issuer yet and picks one up when
		// there is one. An After from Traefik to cert-manager would read as
		// documentation and cost independence.
		{
			Group: Ingress,
			Chart: IngressChart,
			ValuesFrom: func(r *layer.Runner) pulumi.Output {
				return IngressData(r.Cluster.NodeSubnet)
			},
		},
		{
			// It carries no After, and that absence is the point. The load
			// balancer health-checks a node port, so it converges on its own once
			// Traefik is listening; making it wait for the release would mean an
			// ingress address that does not exist until a chart is healthy.
			//
			// In the table rather than created by hand after Deploy returns, which
			// is where it used to be — that left half of what this group makes
			// outside the enumeration layertest checks, and the half in question
			// is the billable one.
			Group:  Ingress,
			Name:   BalancerComponent,
			Create: createBalancer(provider),
		},
	}
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

// deploy creates the Hetzner provider both groups' Kubernetes resources do not
// need, then the components, then exports what the ingress group publishes.
func deploy(r *layer.Runner) error {
	// A provider of its own, because one resource here is created in Hetzner
	// rather than in Kubernetes. r.Options carries the Kubernetes provider,
	// which the load balancer must not inherit.
	hcloudProvider, err := hetzner.NewProvider(r.Ctx, r.Cluster.HcloudToken)
	if err != nil {
		return err
	}

	deployed, err := r.Deploy(components(hcloudProvider))
	if err != nil {
		return err
	}

	// Read back out of the set, because the exports below need the addresses
	// and Deployed holds resources. The assertion is the price of putting the
	// load balancer in the table, and it is worth paying: what this group
	// creates is now enumerable.
	balancer, ok := deployed[BalancerComponent].(*hcloud.LoadBalancer)
	if !ok {
		return fmt.Errorf("%s was not created as a load balancer", BalancerComponent)
	}

	r.Ctx.Export(OutputAddress, balancer.Ipv4)
	r.Ctx.Export(OutputAddressIPv6, balancer.Ipv6)
	r.Ctx.Export(OutputHostname, r.Cluster.Domain)

	// Exported so the engine awaits it: the records are created inside an
	// apply, and an unconsumed output would swallow any error from it.
	r.Ctx.Export(OutputRecords, records(r, balancer, hcloudProvider))

	return nil
}

func main() {
	layer.Run(deploy)
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

// ── The ingress group's own constants and helpers ─────────────────────────
//
// Below rather than beside the cluster-services ones, because the merge's
// readability rests on the two halves staying legible as halves. The group
// field in the table is what the engine reads; this boundary is what a person
// reads.

// DefaultLoadBalancerType is the smallest Hetzner load balancer. It carries an
// ingress comfortably; the reason to change it is throughput, not
// availability.
const DefaultLoadBalancerType = "lb11"

// LoadBalancerTypeKey is the stack config key that overrides it.
//
// It was `ingress:loadBalancerType` while ingress was its own project. A
// Pulumi config key is namespaced by the PROJECT, so merging the two renames
// it — which is a migration step and not a code change, and docs/configuration.md
// carries both spellings for as long as the experiment lasts.
const LoadBalancerTypeKey = "loadBalancerType"

// ControllerReplicas is how many Traefik pods to run. Two, so losing one node
// does not take ingress with it.
const ControllerReplicas = 2

// IngressChart is the registry key, which is also what the values template is
// named after.
const IngressChart = "traefik"

// Stack outputs of the ingress group. The addresses are the one fact an
// operator needs from it, and there is somewhere to read them from: under the
// cloud controller manager they existed only on a Service's status, so
// `pulumi stack output` had nothing to say about how the cluster is reached.
const (
	OutputAddress     = "ingressIp"
	OutputAddressIPv6 = "ingressIpv6"
	OutputHostname    = "ingressHostname"
	OutputRecords     = "dnsRecords"
)

// RecordsPerDomain is how many RRSets one domain gets: an A and an AAAA.
const RecordsPerDomain = 2

// BalancerComponent is the load balancer's name in the set, and what the
// exports read it back by.
const BalancerComponent = "load-balancer"

// createBalancer puts the load balancer in front of the node ports.
//
// The load balancer is created HERE, through the Hetzner provider, rather than
// by asking the cloud controller manager for one. Both were tried against a
// live cluster; internal/pkg/hetzner.NewIngressLoadBalancer records what the
// CCM route cost. In short: a CCM-managed load balancer is invisible to `plan`
// and `destroy`, and it refuses to target a control-plane node at all.
func createBalancer(provider pulumi.ProviderResource) layer.CreateFunc {
	return func(r *layer.Runner, dependencies []pulumi.Resource) (pulumi.Resource, error) {
		// Said out loud, because this is the one billable resource a platform
		// layer creates and its size is a config decision.
		balancerType := r.StringOr(LoadBalancerTypeKey, DefaultLoadBalancerType)
		// Type only: the location is the cluster's, and the cluster tier
		// reports it. Reading it here would mean resolving an Output to print
		// a word.
		r.Log.Step(BalancerComponent, balancerType)

		return hetzner.NewIngressLoadBalancer(r.Ctx, "ingress", hetzner.IngressLoadBalancerArgs{
			ClusterName:      r.Cluster.ClusterName,
			Location:         r.Cluster.Location,
			NetworkID:        r.Cluster.NetworkID,
			LoadBalancerType: balancerType,
		}, append(layer.DependsOn(dependencies), pulumi.Provider(provider))...)
	}
}

// IngressData resolves the cluster tier's outputs into the template's data.
//
// Separated from the component so a test can assert what the template will be
// given without a Pulumi run.
func IngressData(nodeSubnet pulumi.StringInput) pulumi.Output {
	// One input, so no pulumi.All: it took the typed output, put it in a []any
	// and handed it back to be recovered by index and asserted.
	return pulumix.Apply(nodeSubnet.ToStringOutput(),
		func(subnet string) any {
			return values.Traefik{
				Replicas:   ControllerReplicas,
				NodeSubnet: subnet,
				// The same two constants internal/pkg/hetzner points the load
				// balancer's services and health checks at.
				NodePortHTTP:  platform.IngressNodePortHTTP,
				NodePortHTTPS: platform.IngressNodePortHTTPS,
			}
		})
}

// records points the domain at the load balancer, when there is a domain and
// Hetzner holds its zone.
//
// NOT a component, and that is a limit of the table rather than an oversight.
// A component's membership is decided before anything resolves — order() runs
// first, and Create is called for every entry — so a component can decline on
// CONFIG the way KEDA above does. These records exist only if two
// StackReference outputs say so, and those are known inside an apply and
// nowhere earlier. NewIngressRecords also hands back no resource for a
// component to return, because it looks the zone up and writes two RRSets
// inside that same apply.
//
// Both halves are optional and mean different things, so both are logged. No
// domain is a new environment. A domain with no zone here is a domain hosted
// somewhere else, which is a supported arrangement — the records are then
// somebody else's to write, and this group must not imply otherwise by staying
// silent.
//
// Returns a count rather than nothing, and the caller exports it. An output
// nothing consumes is never resolved, so an error inside this apply would
// never surface — the same reason internal/pkg/layer exports its contract check.
func records(
	r *layer.Runner,
	balancer *hcloud.LoadBalancer,
	provider pulumi.ProviderResource,
) pulumi.IntOutput {
	return pulumix.Cast[pulumi.IntOutput](pulumix.Apply2Err(
		r.Cluster.Domain, r.Cluster.DNSZone,
		func(domain, zone string) (int, error) {
			switch {
			case domain == "":
				r.Log.Skipped("dns", "metadata.domain unset, no records and no Ingress anywhere")

				return 0, nil
			case zone == "":
				r.Log.Skipped("dns", "metadata.dnsZone unset, so "+domain+
					" is hosted elsewhere — point it at the ingressIp output by hand")

				return 0, nil
			}

			r.Log.Step("dns", domain+" in zone "+zone)

			if err := hetzner.NewIngressRecords(r.Ctx, "ingress", hetzner.IngressRecordsArgs{
				Zone:   zone,
				Domain: domain,
				IPv4:   balancer.Ipv4,
				IPv6:   balancer.Ipv6,
			}, pulumi.Provider(provider)); err != nil {
				return 0, err
			}

			// One A and one AAAA.
			return RecordsPerDomain, nil
		}))
}
