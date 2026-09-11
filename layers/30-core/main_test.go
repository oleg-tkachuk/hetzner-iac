package main

import (
	"os"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/layer/layertest"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCertManagerValues_KeepsCRDsOnUninstall(t *testing.T) {
	t.Parallel()

	// Removing the CRDs deletes every Certificate and Issuer in the cluster.
	// That is a far larger action than uninstalling a chart, and not one an
	// uninstall should quietly perform.
	crds, ok := CertManagerValues()["crds"].(pulumi.Map)
	require.True(t, ok)

	assert.Equal(t, pulumi.Bool(true), crds["enabled"])
	assert.Equal(t, pulumi.Bool(true), crds["keep"])
}

func TestMetricsServerValues_AddressesNodesByInternalIP(t *testing.T) {
	t.Parallel()

	// Talos kubelet certificates carry the internal address. The chart default
	// tries the hostname first, which does not resolve — metrics-server starts
	// and every scrape fails, so the autoscaler silently has no metrics.
	args, ok := MetricsServerValues()["args"].(pulumi.StringArray)
	require.True(t, ok)

	found := false

	for _, arg := range args {
		if value, ok := arg.(pulumi.String); ok && string(value) == "--kubelet-preferred-address-types=InternalIP" {
			found = true
		}
	}

	assert.True(t, found, "metrics-server must prefer InternalIP on Talos")
}

func TestMetricsServerValues_SurvivesANodeFailure(t *testing.T) {
	t.Parallel()

	values := MetricsServerValues()

	assert.Equal(t, pulumi.Int(2), values["replicas"])

	budget, ok := values["podDisruptionBudget"].(pulumi.Map)
	require.True(t, ok)
	assert.Equal(t, pulumi.Bool(true), budget["enabled"])
}

func TestIssuerSpec_UsesTheProductionACMEEndpoint(t *testing.T) {
	t.Parallel()

	// The staging endpoint issues untrusted certificates, so reaching for it
	// "to be safe" produces browser warnings that read as a misconfiguration.
	acme := acmeSection(t, IssuerSpec("ops@example.test"))

	assert.Equal(t, pulumi.String(LetsEncryptDirectory), acme["server"])
	assert.Contains(t, string(LetsEncryptDirectory), "acme-v02.api.letsencrypt.org")
}

func TestIssuerSpec_CarriesTheContactEmail(t *testing.T) {
	t.Parallel()

	// Let's Encrypt rejects an order with no contact, and sends expiry
	// warnings to this address.
	acme := acmeSection(t, IssuerSpec("ops@example.test"))

	assert.Equal(t, pulumi.String("ops@example.test"), acme["email"])
}

func TestIssuerSpec_SolvesOverTheNginxIngressClass(t *testing.T) {
	t.Parallel()

	// HTTP-01 validation is served by the ingress controller from 40-ingress.
	// A different class name here means orders that never validate.
	acme := acmeSection(t, IssuerSpec("ops@example.test"))

	solvers, ok := acme["solvers"].(pulumi.Array)
	require.True(t, ok)
	require.Len(t, solvers, 1)

	solver, ok := solvers[0].(pulumi.Map)
	require.True(t, ok)

	http01, ok := solver["http01"].(pulumi.Map)
	require.True(t, ok)

	ingress, ok := http01["ingress"].(pulumi.Map)
	require.True(t, ok)

	assert.Equal(t, pulumi.String("nginx"), ingress["ingressClassName"])
}

func TestExternalSecretsValues_InstallsItsCRDs(t *testing.T) {
	t.Parallel()

	// Without them an ExternalSecret is rejected by the API server, which
	// looks like a broken manifest rather than a missing install step.
	assert.Equal(t, pulumi.Bool(true), ExternalSecretsValues()["installCRDs"])
}

func TestNoLayerCreatesServiceMonitors(t *testing.T) {
	t.Parallel()

	// 60-observability owns the Prometheus operator CRDs. A ServiceMonitor
	// here would make this layer fail on a cluster without that one, breaking
	// the independence the layout exists for.
	certManager, ok := CertManagerValues()["prometheus"].(pulumi.Map)
	require.True(t, ok)

	monitor, ok := certManager["servicemonitor"].(pulumi.Map)
	require.True(t, ok)
	assert.Equal(t, pulumi.Bool(false), monitor["enabled"])

	externalSecrets, ok := ExternalSecretsValues()["serviceMonitor"].(pulumi.Map)
	require.True(t, ok)
	assert.Equal(t, pulumi.Bool(false), externalSecrets["enabled"])
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
	// pkg/hetzner turns on rotate-server-certificates and the kubelet asks the
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
