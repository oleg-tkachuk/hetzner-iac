package main

import (
	"os"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/layer/layertest"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/values"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// chartValues is the values YAML a chart's template renders to, parsed.
//
// Through the template rather than around it: the rendered file is what
// reaches Helm, so a test reading a Go map would check something the chart
// never sees.
func chartValues(t *testing.T, chart string, data any) map[string]any {
	t.Helper()

	text, err := values.Render(chart, data)
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(text), &out), "%s must render valid yaml", chart)

	return out
}

func nestedMap(t *testing.T, in map[string]any, key string) map[string]any {
	t.Helper()

	out, ok := in[key].(map[string]any)
	require.True(t, ok, "no map at %q", key)

	return out
}

func TestCertManagerValues_KeepsCRDsOnUninstall(t *testing.T) {
	t.Parallel()

	// Removing the CRDs deletes every Certificate and Issuer in the cluster.
	// That is a far larger action than uninstalling a chart, and not one an
	// uninstall should quietly perform.
	crds := nestedMap(t, chartValues(t, "cert-manager", nil), "crds")

	assert.Equal(t, true, crds["enabled"])
	assert.Equal(t, true, crds["keep"])
}

func TestMetricsServerValues_AddressesNodesByInternalIP(t *testing.T) {
	t.Parallel()

	// Talos kubelet certificates carry the internal address. The chart default
	// tries the hostname first, which does not resolve — metrics-server starts
	// and every scrape fails, so the autoscaler silently has no metrics.
	args, ok := chartValues(t, "metrics-server", MetricsServerData())["args"].([]any)
	require.True(t, ok)

	assert.Contains(t, args, "--kubelet-preferred-address-types=InternalIP",
		"metrics-server must prefer InternalIP on Talos")
}

func TestMetricsServerValues_SurvivesANodeFailure(t *testing.T) {
	t.Parallel()

	rendered := chartValues(t, "metrics-server", MetricsServerData())

	assert.Equal(t, float64(MetricsServerReplicas), rendered["replicas"])
	assert.Equal(t, true, nestedMap(t, rendered, "podDisruptionBudget")["enabled"])
}

func TestIssuerSpec_UsesTheProductionACMEEndpoint(t *testing.T) {
	t.Parallel()

	// The default, because the staging endpoint issues untrusted certificates
	// and reaching for it "to be safe" produces browser warnings that read as
	// a misconfiguration.
	acme := acmeSection(t, IssuerSpec("ops@example.test", false))

	assert.Equal(t, pulumi.String(LetsEncryptProduction), acme["server"])
	assert.Contains(t, LetsEncryptProduction, "acme-v02.api.letsencrypt.org")
}

// TestIssuerSpec_StagingIsAWholeSwitch covers the half that is easy to get
// wrong: an ACME account is registered with ONE directory, so a key
// registered against staging is not an account at production.
//
// Sharing one account-key secret between the endpoints leaves cert-manager
// re-registering on every switch, and the failure reads as an authorization
// problem rather than as the wrong key.
func TestIssuerSpec_StagingIsAWholeSwitch(t *testing.T) {
	t.Parallel()

	staging := acmeSection(t, IssuerSpec("ops@example.test", true))
	production := acmeSection(t, IssuerSpec("ops@example.test", false))

	assert.Equal(t, pulumi.String(LetsEncryptStaging), staging["server"])
	assert.Contains(t, LetsEncryptStaging, "acme-staging-v02.api.letsencrypt.org")

	stagingKey, ok := staging["privateKeySecretRef"].(pulumi.Map)
	require.True(t, ok)
	productionKey, ok := production["privateKeySecretRef"].(pulumi.Map)
	require.True(t, ok)

	assert.NotEqual(t, productionKey["name"], stagingKey["name"],
		"both endpoints register against the same account key secret")

	// Everything else is the same issuer: the solver still validates over the
	// class 40-ingress registers, so switching endpoints does not quietly
	// change how an order is validated.
	assert.Equal(t, production["solvers"], staging["solvers"])
}

func TestIssuerSpec_CarriesTheContactEmail(t *testing.T) {
	t.Parallel()

	// Let's Encrypt rejects an order with no contact, and sends expiry
	// warnings to this address.
	acme := acmeSection(t, IssuerSpec("ops@example.test", false))

	assert.Equal(t, pulumi.String("ops@example.test"), acme["email"])
}

func TestIssuerSpec_SolvesOverTheClassTheIngressLayerRegisters(t *testing.T) {
	t.Parallel()

	// HTTP-01 validation is served by the ingress controller from 40-ingress,
	// so a different class name here means orders that never validate.
	//
	// This test used to say exactly that and then assert the literal "nginx",
	// which is how it locked the bug in rather than catching it: cert-manager
	// would create an Ingress for the challenge, no controller would own it,
	// and the order would sit pending for ever with no error anywhere. It now
	// reads the class from internal/pkg/platform, which is the only thing that cannot
	// drift from what 40-ingress registers.
	acme := acmeSection(t, IssuerSpec("ops@example.test", false))

	solvers, ok := acme["solvers"].(pulumi.Array)
	require.True(t, ok)
	require.Len(t, solvers, 1)

	solver, ok := solvers[0].(pulumi.Map)
	require.True(t, ok)

	http01, ok := solver["http01"].(pulumi.Map)
	require.True(t, ok)

	ingress, ok := http01["ingress"].(pulumi.Map)
	require.True(t, ok)

	assert.Equal(t, pulumi.String(platform.IngressClass), ingress["ingressClassName"])
}

func TestExternalSecretsValues_InstallsItsCRDs(t *testing.T) {
	t.Parallel()

	// Without them an ExternalSecret is rejected by the API server, which
	// looks like a broken manifest rather than a missing install step.
	assert.Equal(t, true, chartValues(t, "external-secrets", nil)["installCRDs"])
}

func TestNoLayerCreatesServiceMonitors(t *testing.T) {
	t.Parallel()

	// Nothing in this repository installs the Prometheus
	// operator CRDs any more — observability is deployed through Argo CD — so
	// a ServiceMonitor here is a resource whose kind does not exist, and the
	// layer would fail on a cluster that has not been given one.
	certManager := nestedMap(t, chartValues(t, "cert-manager", nil), "prometheus")

	assert.Equal(t, false, nestedMap(t, certManager, "servicemonitor")["enabled"])

	externalSecrets := nestedMap(t, chartValues(t, "external-secrets", nil), "serviceMonitor")
	assert.Equal(t, false, externalSecrets["enabled"])
}

func acmeSection(t *testing.T, spec pulumi.Map) pulumi.Map {
	t.Helper()

	acme, ok := spec["acme"].(pulumi.Map)
	require.True(t, ok)

	return acme
}

func TestComponents(t *testing.T) {
	t.Parallel()

	layertest.Check(t, Components)
}

func TestComponents_MetricsServerFollowsTheCertApprover(t *testing.T) {
	t.Parallel()

	// metrics-server scrapes the kubelet over TLS and verifies the
	// certificate. Talos self-signs that certificate without IP SANs, so
	// internal/pkg/hetzner turns on rotate-server-certificates and the kubelet asks the
	// cluster CA instead — but nothing approves those requests on its own.
	//
	// Without this ordering metrics-server fails every scrape, never becomes
	// Ready, and Helm waits out its whole timeout: measured at 611 seconds
	// before `atomic` rolled the release back and took the layer with it.
	var found bool

	for _, component := range Components {
		if component.Chart != "metrics-server" {
			continue
		}

		found = true

		assert.Contains(t, component.After, CertApproverComponent,
			"metrics-server cannot be Ready before the kubelet has a signed serving certificate")
	}

	assert.True(t, found, "metrics-server is no longer in this layer")
}

func TestCertApproverManifest_IsVendoredAndPinned(t *testing.T) {
	t.Parallel()

	// Vendored rather than fetched at apply time, and the image inside pinned:
	// this manifest grants a controller permission to approve certificate
	// signing requests, which is not a thing to resolve from a moving
	// reference.
	raw, err := os.ReadFile(CertApproverManifest)
	require.NoError(t, err, "the manifest the component applies must be committed")

	body := string(raw)

	assert.Contains(t, body, "kind: Deployment")
	assert.Contains(t, body, "kind: ClusterRole")
	assert.Regexp(t, `image: \S+kubelet-serving-cert-approver:\d+\.\d+\.\d+`, body,
		"the image must be pinned to an exact version, not a floating tag")
	assert.Contains(t, body, "v0.12.0", "the provenance comment must name the tag it came from")
}
