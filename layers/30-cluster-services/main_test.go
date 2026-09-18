package main

import (
	"os"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/layer"
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

	layertest.Check(t, testComponents())
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

	for _, component := range testComponents() {
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
func TestComponents_KedaIsTheOnlyOptionalChart(t *testing.T) {
	t.Parallel()

	var optional []string

	for _, component := range testComponents() {
		if component.When != nil {
			optional = append(optional, component.Key())
		}
	}

	assert.Equal(t, []string{KedaChart}, optional)
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

	for _, component := range testComponents() {
		if component.Key() == KedaChart {
			assert.Empty(t, component.After)

			return
		}
	}

	t.Fatalf("no %s component in the set", KedaChart)
}

// testComponents is the set with a nil Hetzner provider.
//
// Nil is safe here and only here: every assertion below reads the TABLE — its
// order, its groups, its After — and none of them creates a resource. A test
// that needed the provider would need a Pulumi run, which is what
// layers/.../main_test.go deliberately does not do.
func testComponents() layer.Components {
	return components(nil)
}

// TestComponents_EveryEntryNamesItsGroup is what makes the merge legible.
//
// An entry with no group sits directly under the stack, indistinguishable in
// the state from either former layer — which is the one outcome merging two
// projects must not produce. Easy to forget when adding a component, and
// nothing else would notice.
func TestComponents_EveryEntryNamesItsGroup(t *testing.T) {
	t.Parallel()

	for _, component := range testComponents() {
		assert.False(t, component.Group.Empty(),
			"component %q names no group, so nothing in the state says which of the two "+
				"former layers it belongs to", component.Key())
	}
}

// TestComponents_TheTwoGroupsHoldWhatTheyUsedTo pins the split itself.
//
// The point of the experiment is that merging the projects did not merge the
// SETS. If a component drifts from one group to the other, `--target` on a
// group stops meaning what the layer it replaced meant.
func TestComponents_TheTwoGroupsHoldWhatTheyUsedTo(t *testing.T) {
	t.Parallel()

	held := map[string][]string{}
	for _, component := range testComponents() {
		held[component.Group.Name] = append(held[component.Group.Name], component.Key())
	}

	assert.ElementsMatch(t, []string{
		"cert-manager", platform.IssuerName, "external-secrets",
		CertApproverComponent, KedaChart, "metrics-server",
	}, held[ClusterServices.Name])

	assert.ElementsMatch(t, []string{IngressChart, BalancerComponent}, held[Ingress.Name])
}

// TestComponents_TheGroupsHaveDistinctTypes is the property a shared type
// would silently destroy: the type is what lands in a child's URN, so two
// groups sharing one are one group as far as the state and `--target` are
// concerned.
func TestComponents_TheGroupsHaveDistinctTypes(t *testing.T) {
	t.Parallel()

	assert.NotEqual(t, ClusterServices.Type, Ingress.Type)
	assert.NotEmpty(t, ClusterServices.Type)
	assert.NotEmpty(t, Ingress.Type)
}

// TestComponents_NothingCrossesTheGroupBoundary holds the independence claim
// at this layer too, not only in internal/pkg/layer.
//
// order() refuses a cross-group After, so a violation is an error rather than a
// silent coupling — but the error arrives at apply time. This says it at test
// time, and names the pair.
func TestComponents_NothingCrossesTheGroupBoundary(t *testing.T) {
	t.Parallel()

	groupOf := map[string]string{}
	for _, component := range testComponents() {
		groupOf[component.Key()] = component.Group.Name
	}

	for _, component := range testComponents() {
		for _, after := range component.After {
			assert.Equal(t, groupOf[component.Key()], groupOf[after],
				"%q follows %q across a group boundary: neither group is then independently "+
					"appliable, and `destroy --target` on one takes a resource from the other",
				component.Key(), after)
		}
	}
}

// TestIngressValues_CarryThePinnedNodePorts is the ingress group's half of the
// contract with internal/pkg/hetzner, moved here with the code it belongs to.
func TestIngressValues_CarryThePinnedNodePorts(t *testing.T) {
	t.Parallel()

	rendered := chartValues(t, IngressChart, values.Traefik{
		Replicas:      ControllerReplicas,
		NodeSubnet:    "10.0.1.0/24",
		NodePortHTTP:  platform.IngressNodePortHTTP,
		NodePortHTTPS: platform.IngressNodePortHTTPS,
	})

	ports := nestedMap(t, rendered, "ports")
	assert.Equal(t, float64(platform.IngressNodePortHTTP), nestedMap(t, ports, "web")["nodePort"])
	assert.Equal(t, float64(platform.IngressNodePortHTTPS), nestedMap(t, ports, "websecure")["nodePort"])
}
