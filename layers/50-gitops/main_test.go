package main

import (
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/layer/layertest"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/platform"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/values"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// testDomain is a name the Ingress branch can render against.
const testDomain = "argocd.example.test"

// render is the values YAML this layer would hand Helm, parsed.
func render(t *testing.T, domain string) map[string]any {
	t.Helper()

	text, err := values.Render("argo-cd", ArgoCDData(domain))
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(text), &out), "argo-cd must render valid yaml")

	return out
}

func nested(t *testing.T, in map[string]any, keys ...string) map[string]any {
	t.Helper()

	for _, key := range keys {
		next, ok := in[key].(map[string]any)
		require.True(t, ok, "no map at %q", key)

		in = next
	}

	return in
}

func TestArgoCDValues_NoIngressWithoutADomain(t *testing.T) {
	t.Parallel()

	// Before DNS exists, the right shape is no Ingress at all: the UI is
	// reachable with port-forward and nothing is published by accident. The
	// template's conditional is what does it, so this is also what catches a
	// block that renders unconditionally.
	assert.NotContains(t, nested(t, render(t, ""), "server"), "ingress")
}

func TestArgoCDValues_IngressWhenADomainIsGiven(t *testing.T) {
	t.Parallel()

	ingress := nested(t, render(t, testDomain), "server", "ingress")

	assert.Equal(t, true, ingress["enabled"])
	assert.Equal(t, testDomain, ingress["hostname"])
	assert.Equal(t, true, ingress["tls"])
}

func TestArgoCDValues_AsksForTheClassTheIngressLayerRegisters(t *testing.T) {
	t.Parallel()

	// This was the literal "nginx" and stopped being true the moment Traefik
	// replaced ingress-nginx. An Ingress naming a class no controller owns is
	// accepted by the API server and then ignored: the resource exists, looks
	// right, and routes nothing.
	ingress := nested(t, render(t, testDomain), "server", "ingress")

	assert.Equal(t, platform.IngressClass, ingress["ingressClassName"])
}

func TestArgoCDValues_RequestsCertificatesFromTheClusterIssuer(t *testing.T) {
	t.Parallel()

	// The annotation must name the ClusterIssuer created by 30-core; a
	// different name leaves the Ingress with no certificate and no error.
	annotations := nested(t, render(t, testDomain), "server", "ingress", "annotations")

	assert.Equal(t, IssuerName, annotations["cert-manager.io/cluster-issuer"])
	assert.Equal(t, "letsencrypt", IssuerName,
		"must match the ClusterIssuer name in 30-core")
}

func TestArgoCDValues_TerminatesTLSAtTheIngressOnly(t *testing.T) {
	t.Parallel()

	// TLS on both sides produces a redirect loop. The two settings below are a
	// pair — changing one alone is what causes it.
	rendered := render(t, testDomain)

	args, ok := nested(t, rendered, "server")["extraArgs"].([]any)
	require.True(t, ok)
	assert.Equal(t, []any{"--insecure"}, args)

	assert.Equal(t, true, nested(t, rendered, "configs", "params")["server.insecure"])
}

func TestArgoCDValues_APIAndRepoServerSurviveANodeFailure(t *testing.T) {
	t.Parallel()

	// The application controller is deliberately not among them: sharding it
	// needs configuration that only pays off with many applications.
	rendered := render(t, "")

	assert.Equal(t, float64(StatelessReplicas), nested(t, rendered, "server")["replicas"])
	assert.Equal(t, float64(StatelessReplicas), nested(t, rendered, "repoServer")["replicas"])
	assert.Equal(t, float64(1), nested(t, rendered, "controller")["replicas"])
}

func TestArgoCDValues_CreatesNoServiceMonitors(t *testing.T) {
	t.Parallel()

	// layers/60-observability owns the Prometheus operator CRDs. One here
	// would make this layer fail on a cluster where that layer is absent.
	rendered := render(t, "")

	for _, component := range []string{"controller", "repoServer", "server"} {
		assert.Equal(t, false,
			nested(t, rendered, component, "metrics", "serviceMonitor")["enabled"], component)
	}
}

func TestComponents(t *testing.T) {
	t.Parallel()

	layertest.Check(t, Components)
}
