package main

import (
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/charts"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/layer"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/layer/layertest"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/values"

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

	// The annotation must name the ClusterIssuer created by
	// 30-cluster-services; a
	// different name leaves the Ingress with no certificate and no error.
	annotations := nested(t, render(t, testDomain), "server", "ingress", "annotations")

	assert.Equal(t, IssuerName, annotations["cert-manager.io/cluster-issuer"])
	assert.Equal(t, "letsencrypt", IssuerName,
		"must match the ClusterIssuer name in 30-cluster-services")
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

	// Nothing in this repository installs the Prometheus
	// operator CRDs any more — observability is deployed through Argo CD — so
	// a ServiceMonitor here is a resource whose kind does not exist, and the
	// layer would fail on a cluster that has not been given one.
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

// TestRootApplicationSpec_PointsAtWhatConfigNamed is the contract between
// three config keys and what Argo CD is actually told.
func TestRootApplicationSpec_PointsAtWhatConfigNamed(t *testing.T) {
	t.Parallel()

	spec := RootApplicationSpec("https://example.com/estate.git", "deploy/apps", "v1.2.3")

	source, ok := spec["source"].(map[string]any)
	require.True(t, ok, "the spec carries no source")

	assert.Equal(t, "https://example.com/estate.git", source["repoURL"])
	assert.Equal(t, "deploy/apps", source["path"])
	assert.Equal(t, "v1.2.3", source["targetRevision"],
		"Argo CD reads the revision from targetRevision; any other key is silently ignored")

	assert.Equal(t, RootProject, spec["project"],
		"a root in no project of its own runs under `default`, which permits everything")
}

// TestRootApplicationSpec_SyncsItselfAndPrunes is the decision that makes it
// GitOps rather than a bookmark.
func TestRootApplicationSpec_SyncsItselfAndPrunes(t *testing.T) {
	t.Parallel()

	policy, ok := RootApplicationSpec("https://example.com/e.git", ".", "HEAD")["syncPolicy"].(map[string]any)
	require.True(t, ok, "the root has no syncPolicy, so nothing syncs without a person")

	automated, ok := policy["automated"].(map[string]any)
	require.True(t, ok, "the root is not automated")

	assert.Equal(t, true, automated["prune"],
		"without prune, a child Application deleted from git stays in the cluster for ever")
	assert.Equal(t, true, automated["selfHeal"],
		"without selfHeal, a hand edit in the cluster outlives the commit that contradicts it")
}

// TestRootProjectSpec_LetsTheRootCreateOnlyArgoCDObjects is the blast radius.
//
// A root's job is to create more Applications; each child carries its own
// project saying what THAT one may create. A root permitted to create
// arbitrary resources would make every child project decorative.
func TestRootProjectSpec_LetsTheRootCreateOnlyArgoCDObjects(t *testing.T) {
	t.Parallel()

	spec := RootProjectSpec("https://example.com/estate.git")

	cluster, ok := spec["clusterResourceWhitelist"].([]map[string]any)
	require.True(t, ok, "clusterResourceWhitelist is absent, and Argo CD reads absent as EVERYTHING")
	assert.Empty(t, cluster, "the root may create cluster-scoped resources")

	namespaced, ok := spec["namespaceResourceWhitelist"].([]map[string]any)
	require.True(t, ok, "namespaceResourceWhitelist is absent, and Argo CD reads absent as everything")
	require.NotEmpty(t, namespaced)

	for _, allowed := range namespaced {
		assert.Equal(t, "argoproj.io", allowed["group"],
			"the root may create %v, which is not an Argo CD object", allowed)
	}
}

// TestRootProjectSpec_TrustsOnlyTheRepositoryItWasGiven keeps the project from
// being a way to sync anything from anywhere.
func TestRootProjectSpec_TrustsOnlyTheRepositoryItWasGiven(t *testing.T) {
	t.Parallel()

	spec := RootProjectSpec("https://example.com/estate.git")

	repos, ok := spec["sourceRepos"].([]string)
	require.True(t, ok, "sourceRepos is absent, so any repository may be synced under this project")
	assert.Equal(t, []string{"https://example.com/estate.git"}, repos)

	destinations, ok := spec["destinations"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, destinations, 1, "more than one destination is more than a root needs")
	assert.Equal(t, Namespace, destinations[0]["namespace"],
		"the root's children are Argo CD objects and belong in Argo CD's namespace")
}

// TestNamespace_IsWhereTheChartInstalls holds the two halves of a name written
// in Go and chosen by the chart registry.
func TestNamespace_IsWhereTheChartInstalls(t *testing.T) {
	t.Parallel()

	chart, err := charts.Get(Chart)
	require.NoError(t, err)

	assert.Equal(t, chart.Namespace, Namespace,
		"the root Application would be created in %q while Argo CD watches %q",
		Namespace, chart.Namespace)
}

// TestRootDefaults_AreUsableWithoutBeingGuesses covers the two keys an
// operator can leave alone.
func TestRootDefaults_AreUsableWithoutBeingGuesses(t *testing.T) {
	t.Parallel()

	assert.Equal(t, ".", DefaultRootPath, "a path default other than the root would be a guess")
	assert.Equal(t, "HEAD", DefaultRootRevision,
		"a branch name as the default would be a decision about somebody else's repository")
}

// TestComponents_TheRootFollowsArgoCD is the ordering that matters: the CRDs
// the root is made of arrive with the chart.
func TestComponents_TheRootFollowsArgoCD(t *testing.T) {
	t.Parallel()

	var root *layer.Component

	for i := range Components {
		if Components[i].Key() == RootApplication {
			root = &Components[i]
		}
	}

	require.NotNil(t, root, "no root component in the set")
	assert.Contains(t, root.After, Chart,
		"an Application applied before the chart is an unknown kind, and the apply fails")
	assert.Empty(t, root.Chart, "the root is not a chart")
}
