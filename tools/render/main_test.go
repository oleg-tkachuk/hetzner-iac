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
