package main

import (
	"os"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/layer/layertest"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/values"

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

	assert.Equal(t, LetsEncryptProduction, acme["server"])
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

	assert.Equal(t, LetsEncryptStaging, staging["server"])
	assert.Contains(t, LetsEncryptStaging, "acme-staging-v02.api.letsencrypt.org")

	stagingKey, ok := staging["privateKeySecretRef"].(map[string]any)
	require.True(t, ok)
	productionKey, ok := production["privateKeySecretRef"].(map[string]any)
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

	assert.Equal(t, "ops@example.test", acme["email"])
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

	solvers, ok := acme["solvers"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, solvers, 1)

	http01, ok := solvers[0]["http01"].(map[string]any)
	require.True(t, ok)

	ingress, ok := http01["ingress"].(map[string]any)
	require.True(t, ok)

	assert.Equal(t, platform.IngressClass, ingress["ingressClassName"])
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

func acmeSection(t *testing.T, spec map[string]any) map[string]any {
	t.Helper()

	acme, ok := spec["acme"].(map[string]any)
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
	// internal/pkg/clusterspec turns on rotate-server-certificates and the kubelet asks the
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

// TestParseEnabled_RefusesAValueThatIsNotABoolean is the behaviour the switch
// exists with rather than without.
//
// `kedaEnabled: yes` read as false gives a cluster with no autoscaler and an
// operator who believes otherwise — the two states look identical from
// outside, which is why this is an error and not a default.
func TestParseEnabled_RefusesAValueThatIsNotABoolean(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		value   string
		want    bool
		wantErr bool
	}{
		"unset is off":            {value: "", want: false},
		"true":                    {value: "true", want: true},
		"false":                   {value: "false", want: false},
		"1 is what strconv takes": {value: "1", want: true},
		"0":                       {value: "0", want: false},
		// The one that costs a cluster: a shell-ism, and YAML's own word for
		// true, which strconv.ParseBool does not accept.
		"yes is refused, not read as false": {value: "yes", wantErr: true},
		"on is refused too":                 {value: "on", wantErr: true},
		"a typo is refused":                 {value: "ture", wantErr: true},
	}

	for name, one := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			enabled, err := parseEnabled(KedaEnabledKey, one.value)

			if one.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), KedaEnabledKey,
					"the failure does not name the key the operator has to fix")
				assert.Contains(t, err.Error(), one.value,
					"the failure does not quote what was actually set")

				return
			}

			require.NoError(t, err)
			assert.Equal(t, one.want, enabled)
		})
	}
}

// TestComponents_KedaIsTheOnlyOptionalChart pins the shape rather than the
// decision: every other chart in this layer is installed unconditionally, and
// a When appearing on one of those would mean a core service had quietly
// become optional.
//
// Charts and everything else are counted apart, because they answer different
// questions. An optional chart is a service that may be missing. An optional
// component that is not a chart is a piece of wiring that has nothing to wire
// until it is configured — the secret store has no store to point at until a
// token exists, and installing it anyway would leave the operator authenticated
// against nothing.
func TestComponents_KedaIsTheOnlyOptionalChart(t *testing.T) {
	t.Parallel()

	var charts, others []string

	for _, component := range Components {
		if component.When == nil {
			continue
		}

		if component.Chart != "" {
			charts = append(charts, component.Key())

			continue
		}

		others = append(others, component.Key())
	}

	assert.Equal(t, []string{KedaChart}, charts)
	assert.Equal(t, []string{SecretStoreComponent}, others,
		"a second optional non-chart component is a decision worth naming here")
}

// TestComponents_KedaWaitsForNothing holds the reasoning in its comment to the
// literal.
//
// Not cert-manager: KEDA signs its own certificates and patches the
// APIService's CA bundle itself. Not metrics-server: it serves a different API
// group. An After here would be a dependency bought for nothing, and it would
// make KEDA's absence able to delay the rest of the layer.
func TestComponents_KedaWaitsForNothing(t *testing.T) {
	t.Parallel()

	for _, component := range Components {
		if component.Key() == KedaChart {
			assert.Empty(t, component.After)

			return
		}
	}

	t.Fatalf("no %s component in the set", KedaChart)
}

// TestSecretStoreSpec_PointsAtOneEnvironmentWithACredential reads the spec the
// CRD accepts, field by field, because an untyped CustomResource has nothing
// else checking it: a misspelled key is not a compile error and not a Pulumi
// error — the API server takes the object, and the store never becomes Ready.
func TestSecretStoreSpec_PointsAtOneEnvironmentWithACredential(t *testing.T) {
	t.Parallel()

	spec := SecretStoreSpec("acme", "prod")

	provider, ok := spec["provider"].(map[string]any)
	require.True(t, ok)

	esc, ok := provider["pulumi"].(map[string]any)
	require.True(t, ok, "the provider key must be the one the CRD names")

	// The three the CRD marks required. One environment per stack: `prod` must
	// not be able to read `dev`.
	assert.Equal(t, "acme", esc["organization"])
	assert.Equal(t, platform.SecretStoreProject, esc["project"])
	assert.Equal(t, "prod", esc["environment"])

	auth, ok := esc["auth"].(map[string]any)
	require.True(t, ok, "auth, not the deprecated accessToken field beside it")

	token, ok := auth["accessToken"].(map[string]any)
	require.True(t, ok)

	ref, ok := token["secretRef"].(map[string]any)
	require.True(t, ok)

	assert.Equal(t, platform.SecretStoreTokenSecret, ref["name"])
	assert.Equal(t, platform.SecretStoreTokenKey, ref["key"])
	assert.Equal(t, platform.SecretStoreNamespace, ref["namespace"],
		"a cluster-scoped store with no namespace on its secretRef looks in the "+
			"ExternalSecret's namespace and finds nothing")

	// The deprecated shape must not be there as well: the CRD accepts either,
	// and carrying both is how the wrong one survives a review.
	assert.NotContains(t, esc, "accessToken")
}

// TestHasAccessToken_DecidesOnEmptinessAlone keeps the operator from being
// installed and pointed at nothing, and keeps this from growing an opinion
// about what a token looks like.
func TestHasAccessToken_DecidesOnEmptinessAlone(t *testing.T) {
	t.Parallel()

	for name, one := range map[string]struct {
		token string
		want  bool
	}{
		"unset means no store": {token: "", want: false},
		// Not a token-shaped string on purpose: a realistic one trips the
		// secret scanner, and this test is about emptiness.
		"any value means one": {token: "set", want: true},
		// Deliberately true: validating the shape here would be a second
		// opinion about what pulumi config stored, and a wrong token already
		// fails loudly at the store.
		"whitespace is a value": {token: " ", want: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, one.want, HasAccessToken(one.token))
		})
	}
}
