package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseWorkloads(t *testing.T) {
	t.Parallel()

	manifest := []byte(`---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: cilium-operator
  namespace: kube-system
spec:
  template:
    spec:
      containers:
        - name: cilium-operator
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: cilium
spec:
  template:
    spec:
      volumes:
        - name: cilium-run
`)

	found := parseWorkloads(manifest)

	assert.True(t, found["Deployment/cilium-operator"])
	assert.True(t, found["DaemonSet/cilium"])
	assert.Len(t, found, 2, "container and volume names must not be mistaken for workloads")
}

func TestParseWorkloads_IgnoresNonWorkloadKinds(t *testing.T) {
	t.Parallel()

	// A rendered chart is mostly ConfigMaps, Services, RBAC and CRDs. Counting
	// their names would make the check pass on a chart that renders no
	// workloads at all.
	manifest := []byte(`---
kind: Service
metadata:
  name: cilium-agent
---
kind: ConfigMap
metadata:
  name: cilium-config
---
kind: CustomResourceDefinition
metadata:
  name: ciliumnetworkpolicies.cilium.io
`)

	assert.Empty(t, parseWorkloads(manifest))
}

func TestParseWorkloads_TakesOnlyTheFirstNameAfterAKind(t *testing.T) {
	t.Parallel()

	// The pattern that would break a naive scanner: a workload whose pod
	// template carries names at the same indentation further down.
	manifest := []byte(`kind: StatefulSet
metadata:
  name: loki
spec:
  serviceName: loki-headless
  template:
    metadata:
      name: should-not-be-picked-up
`)

	found := parseWorkloads(manifest)

	require.Len(t, found, 1)
	assert.True(t, found["StatefulSet/loki"])
}

func TestParseWorkloads_Empty(t *testing.T) {
	t.Parallel()

	assert.Empty(t, parseWorkloads(nil))
	assert.Empty(t, parseWorkloads([]byte("")))
}

func TestWriteValues(t *testing.T) {
	t.Parallel()

	// Loki needs a nested schemaConfig the --set form cannot express, so its
	// file must exist and be non-empty.
	path, err := writeValues("loki")
	require.NoError(t, err)
	require.NotEmpty(t, path)

	t.Cleanup(func() { _ = os.Remove(path) })

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(content), "SingleBinary")
	assert.Contains(t, string(content), "schemaConfig")
}

func TestWriteValues_NoFileMeansNoOverrides(t *testing.T) {
	t.Parallel()

	// A chart whose defaults already match renders with no values file, and
	// that is not an error.
	path, err := writeValues("cilium")
	require.NoError(t, err)
	assert.Empty(t, path)
}

func TestSortedKeys(t *testing.T) {
	t.Parallel()

	// Sorted so a failure report reads the same on every run.
	got := sortedKeys(map[string]bool{"z": true, "a": true, "m": true})
	assert.Equal(t, []string{"a", "m", "z"}, got)
}

func TestKubeconformArgs_NeverIgnoresMissingSchemas(t *testing.T) {
	t.Parallel()

	// The regression that would make the whole check worthless.
	//
	// -ignore-missing-schemas reads like the obvious way to let custom
	// resources through, and it is how this was first written. But a REMOVED
	// api version has no schema either, so `policy/v1beta1 PodSecurityPolicy`
	// came back "Skipped" and kubeconform exited 0 — measured, against the
	// very failure class this check exists for. Kubernetes v1.36 removing a
	// kube-apiserver flag already cost this repository a control plane.
	//
	// Named kinds instead: a chart that starts emitting a new custom resource
	// fails loudly and somebody adds it on purpose.
	args := kubeconformArgs("1.36.4")

	assert.NotContains(t, args, "-ignore-missing-schemas",
		"a removed api version has no schema either, so this flag hides exactly what the check is for")
	assert.Contains(t, args, "-strict")
	assert.Contains(t, args, "1.36.4")
}

func TestSkippedKinds_AreCustomResourcesOnly(t *testing.T) {
	t.Parallel()

	// Every entry must be something Kubernetes does not define itself, or the
	// list is quietly hiding a built-in. CustomResourceDefinition is the one
	// exception and the reason is upstream: the strict standalone schema set
	// does not publish one.
	builtins := map[string]bool{
		"Deployment": true, "StatefulSet": true, "DaemonSet": true,
		"Service": true, "ConfigMap": true, "Secret": true, "Job": true,
		"Ingress": true, "PodSecurityPolicy": true, "CronJob": true,
	}

	for _, kind := range skippedKinds {
		assert.False(t, builtins[kind],
			"%s is a built-in kind: skipping it hides a removed api version rather than a missing CRD schema", kind)
	}

	assert.Contains(t, skippedKinds, "CustomResourceDefinition")
}
