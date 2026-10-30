package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/charts"
)

func selectorOf(namespace string, labels map[string]string) metav1.LabelSelector {
	m := map[string]string{namespaceLabel: namespace}
	for k, v := range labels {
		m[ciliumLabelPrefix+k] = v
	}

	return metav1.LabelSelector{MatchLabels: m}
}

var ccm = renderedPod{namespace: "kube-system", labels: map[string]string{"app.kubernetes.io/name": "hcloud-cloud-controller-manager"}}

func TestMatches_ReadsCiliumsLabelKeys(t *testing.T) {
	t.Parallel()

	assert.True(t, matches(selectorOf("kube-system", map[string]string{"app.kubernetes.io/name": "hcloud-cloud-controller-manager"}), ccm))
	assert.False(t, matches(selectorOf("traefik", map[string]string{"app.kubernetes.io/name": "hcloud-cloud-controller-manager"}), ccm),
		"the namespace is part of the selector")
	assert.False(t, matches(selectorOf("kube-system", map[string]string{"app.kubernetes.io/name": "hcloud-ccm"}), ccm))
}

// An In list naming two pods must find both: one still matching hid the other
// having gone.
func TestUnmatchedSelectors_LooksForEachAlternative(t *testing.T) {
	t.Parallel()

	selector := metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
		{Key: namespaceLabel, Operator: selectorIn, Values: []string{"kube-system"}},
		{Key: ciliumLabelPrefix + "app.kubernetes.io/name", Operator: selectorIn, Values: []string{"hcloud-csi", "hcloud-cloud-controller-manager"}},
	}}

	problems := unmatchedSelectors([]namedSelector{{"60-allow-hcloud-api.yaml allow-hcloud-api", selector}}, []renderedPod{ccm})
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0], "hcloud-csi")

	csi := renderedPod{namespace: "kube-system", labels: map[string]string{"app.kubernetes.io/name": "hcloud-csi"}}
	assert.Empty(t, unmatchedSelectors([]namedSelector{{"x", selector}}, []renderedPod{ccm, csi}))
}

func TestUnmatchedSelectors_SkipsWhatNamesNoPod(t *testing.T) {
	t.Parallel()

	selectors := []namedSelector{
		{"empty", metav1.LabelSelector{}},
		{"namespace only", selectorOf("argocd", nil)},
		{"coredns", selectorOf("kube-system", map[string]string{"k8s-app": "kube-dns"})},
	}

	assert.Empty(t, unmatchedSelectors(selectors, nil),
		"no pod label, or a pod no chart here renders, is not this check's to hold")
}

func TestPolicySelectors_ReadsEveryEndpointSelector(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "10-x.yaml"), []byte(`apiVersion: cilium.io/v2
kind: CiliumClusterwideNetworkPolicy
metadata: {name: x}
spec:
  endpointSelector: {matchLabels: {"k8s:io.kubernetes.pod.namespace": traefik}}
  egress:
    - toEndpoints: [{matchLabels: {"k8s:app.kubernetes.io/name": a}}]
  ingress:
    - fromEndpoints: [{matchLabels: {"k8s:app.kubernetes.io/name": b}}]
`), 0o600))

	selectors, err := policySelectors(dir)
	require.NoError(t, err)
	require.Len(t, selectors, 3, "the endpoint selector and one from each direction")
	assert.Equal(t, "10-x.yaml x", selectors[0].where)
}

func TestPodTemplates_TakesTheTemplatesLabelsAndNamespace(t *testing.T) {
	t.Parallel()

	pods, err := podTemplates([]byte(`apiVersion: apps/v1
kind: Deployment
metadata: {name: d}
spec: {template: {metadata: {labels: {app.kubernetes.io/name: traefik}}}}
---
apiVersion: apps/v1
kind: DaemonSet
metadata: {name: n, namespace: kube-system}
spec: {template: {metadata: {labels: {k8s-app: x}}}}
---
apiVersion: v1
kind: Service
metadata: {name: s}
`), "traefik")
	require.NoError(t, err)
	require.Len(t, pods, 2)
	assert.Equal(t, renderedPod{namespace: "traefik", labels: map[string]string{"app.kubernetes.io/name": "traefik"}}, pods[0])
	assert.Equal(t, "kube-system", pods[1].namespace)
}

// writePolicy writes one policy file into a new directory and returns it.
func writePolicy(t *testing.T, policy string) string {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "10-x.yaml"), []byte(policy), 0o600))

	return dir
}

func TestCheckPolicySelectors(t *testing.T) {
	t.Parallel()

	render := renderFixture{manifests: map[string]string{
		charts.Traefik: podManifest("traefik", "traefik:v3"),
	}}.render

	policy := func(name string) string {
		return `apiVersion: cilium.io/v2
kind: CiliumClusterwideNetworkPolicy
metadata: {name: x}
spec:
  endpointSelector: {matchLabels: {"k8s:io.kubernetes.pod.namespace": traefik, "k8s:app.kubernetes.io/name": ` + name + `}}
`
	}

	var out bytes.Buffer

	assert.Zero(t, checkPolicySelectors(t.Context(), &out, render, writePolicy(t, policy("traefik"))))
	assert.Equal(t, "ok    policies       every policy selector names a pod a chart renders\n", out.String())

	out.Reset()

	assert.Equal(t, 1, checkPolicySelectors(t.Context(), &out, render, writePolicy(t, policy("gone"))))
	assert.Contains(t, out.String(), "MISS  policies       10-x.yaml x selects")

	out.Reset()

	assert.Equal(t, 1, checkPolicySelectors(t.Context(), &out, render, writePolicy(t, "kind: [unclosed")))
	assert.Contains(t, out.String(), "MISS  policies       ")
	assert.Contains(t, out.String(), "10-x.yaml")

	out.Reset()

	failing := renderFixture{failing: map[string]bool{charts.Traefik: true}}.render
	assert.Equal(t, 1, checkPolicySelectors(t.Context(), &out, failing, writePolicy(t, policy("traefik"))))
	assert.Equal(t, "MISS  policies       traefik: boom\n", out.String())
}

func TestRenderedPods_NamesTheChartItCouldNotRead(t *testing.T) {
	t.Parallel()

	unreadable := renderFixture{manifests: map[string]string{
		charts.Cilium: "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: d}\nspec: {replicas: many}\n",
	}}.render

	_, err := renderedPods(t.Context(), unreadable)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cilium: ")

	split := renderFixture{manifests: map[string]string{charts.Cilium: "kind: [unclosed"}}.render

	_, err = renderedPods(t.Context(), split)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cilium: ")
}

func TestPodTemplateOf(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet"} {
		template, err := podTemplateOf(kind, []byte("spec: {template: {metadata: {labels: {a: b}}}}"))
		require.NoError(t, err, kind)
		assert.Equal(t, map[string]string{"a": "b"}, template.Labels, kind)
	}

	template, err := podTemplateOf("Job", []byte("spec: {template: {metadata: {labels: {a: b}}}}"))
	require.NoError(t, err)
	assert.Nil(t, template, "a kind this check does not track has no template here")
}
