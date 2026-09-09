package main

import (
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestArgoCDValues_NoIngressWithoutADomain(t *testing.T) {
	t.Parallel()

	// Before DNS exists, the right shape is no Ingress at all: the UI is
	// reachable with port-forward and nothing is published by accident.
	server, ok := ArgoCDValues("")["server"].(pulumi.Map)
	require.True(t, ok)

	assert.NotContains(t, server, "ingress")
}

func TestArgoCDValues_IngressWhenADomainIsGiven(t *testing.T) {
	t.Parallel()

	server, ok := ArgoCDValues("argocd.example.test")["server"].(pulumi.Map)
	require.True(t, ok)

	ingress, ok := server["ingress"].(pulumi.Map)
	require.True(t, ok)

	assert.Equal(t, pulumi.Bool(true), ingress["enabled"])
	assert.Equal(t, pulumi.String("argocd.example.test"), ingress["hostname"])
	assert.Equal(t, pulumi.String("nginx"), ingress["ingressClassName"])
	assert.Equal(t, pulumi.Bool(true), ingress["tls"])
}

func TestArgoCDValues_RequestsCertificatesFromTheClusterIssuer(t *testing.T) {
	t.Parallel()

	// The annotation must name the ClusterIssuer created by 30-core; a
	// different name leaves the Ingress with no certificate and no error.
	server, _ := ArgoCDValues("argocd.example.test")["server"].(pulumi.Map)
	ingress, _ := server["ingress"].(pulumi.Map)

	annotations, ok := ingress["annotations"].(pulumi.Map)
	require.True(t, ok)

	assert.Equal(t, pulumi.String("letsencrypt"), annotations["cert-manager.io/cluster-issuer"])
	assert.Equal(t, "letsencrypt", IssuerName,
		"must match the ClusterIssuer name in 30-core")
}

func TestArgoCDValues_TerminatesTLSAtTheIngressOnly(t *testing.T) {
	t.Parallel()

	// TLS on both sides produces a redirect loop. The two settings below are a
	// pair — changing one alone is what causes it.
	values := ArgoCDValues("argocd.example.test")

	server, _ := values["server"].(pulumi.Map)

	args, ok := server["extraArgs"].(pulumi.StringArray)
	require.True(t, ok)
	require.Len(t, args, 1)
	assert.Equal(t, pulumi.String("--insecure"), args[0])

	configs, ok := values["configs"].(pulumi.Map)
	require.True(t, ok)

	params, ok := configs["params"].(pulumi.Map)
	require.True(t, ok)
	assert.Equal(t, pulumi.Bool(true), params["server.insecure"])
}

func TestArgoCDValues_APIAndRepoServerSurviveANodeFailure(t *testing.T) {
	t.Parallel()

	values := ArgoCDValues("")

	server, _ := values["server"].(pulumi.Map)
	assert.Equal(t, pulumi.Int(2), server["replicas"])

	repoServer, ok := values["repoServer"].(pulumi.Map)
	require.True(t, ok)
	assert.Equal(t, pulumi.Int(2), repoServer["replicas"])
}

func TestArgoCDValues_CreatesNoServiceMonitors(t *testing.T) {
	t.Parallel()

	// 60-observability owns the Prometheus operator CRDs.
	values := ArgoCDValues("")

	for _, component := range []string{"controller", "repoServer", "server"} {
		section, ok := values[component].(pulumi.Map)
		require.True(t, ok, component)

		metrics, ok := section["metrics"].(pulumi.Map)
		require.True(t, ok, component)

		monitor, ok := metrics["serviceMonitor"].(pulumi.Map)
		require.True(t, ok, component)

		assert.Equal(t, pulumi.Bool(false), monitor["enabled"], component)
	}
}
