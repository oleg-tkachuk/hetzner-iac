package main

import (
	"os"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/charts"

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

	found := parseWorkloads(manifest, "kube-system")

	assert.True(t, found["Deployment/kube-system/cilium-operator"])
	// No namespace of its own, so it takes the release's — which is what
	// Helm does with it.
	assert.True(t, found["DaemonSet/kube-system/cilium"])
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

	assert.Empty(t, parseWorkloads(manifest, "kube-system"))
}

func TestParseWorkloads_TakesOnlyTheObjectsOwnName(t *testing.T) {
	t.Parallel()

	// The pattern that would break a naive scanner: a workload whose pod
	// template carries names further down.
	manifest := []byte(`kind: StatefulSet
metadata:
  name: loki
spec:
  serviceName: loki-headless
  template:
    metadata:
      name: should-not-be-picked-up
`)

	found := parseWorkloads(manifest, "observability")

	require.Len(t, found, 1)
	assert.True(t, found["StatefulSet/observability/loki"])
}

func TestParseWorkloads_Empty(t *testing.T) {
	t.Parallel()

	assert.Empty(t, parseWorkloads(nil, "kube-system"))
	assert.Empty(t, parseWorkloads([]byte(""), "kube-system"))
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
	// that is not an error. hcloud-csi is one: nothing here configures it.
	path, err := writeValues("hcloud-csi")
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

func TestCheckHostAccess(t *testing.T) {
	t.Parallel()

	// A node-exporter DaemonSet, reduced to the fields the gate reads.
	daemonSet := func(namespace string) string {
		return `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: prometheus-node-exporter
  namespace: ` + namespace + `
spec:
  template:
    spec:
      hostNetwork: true
      hostPID: true
      volumes:
        - hostPath:
            path: /
`
	}

	const grafana = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: grafana
  namespace: observability
spec:
  template:
    spec:
      containers:
        - name: grafana
`

	tests := []struct {
		name      string
		manifests string
		namespace string
		wantErr   bool
	}{
		{
			name:      "host access in an exempt namespace is fine",
			manifests: daemonSet("kube-system"),
			namespace: "kube-system",
		},
		{
			// The deploy this gate exists for: DESIRED 1, CURRENT 0, no pod
			// at all, and Helm waiting out its whole timeout.
			name:      "host access under baseline is refused",
			manifests: daemonSet("observability"),
			namespace: "observability",
			wantErr:   true,
		},
		{
			name:      "no host access needs no exemption",
			manifests: grafana,
			namespace: "observability",
		},
		{
			// Why the check is per document. Checking the release namespace
			// would refuse the whole chart for what one DaemonSet asks.
			name:      "one chart in two namespaces",
			manifests: grafana + "\n---" + daemonSet("kube-system"),
			namespace: "observability",
		},
		{
			// Helm's own behaviour for a document that names no namespace.
			name:      "a document without a namespace takes the release's",
			manifests: daemonSet(""),
			namespace: "observability",
			wantErr:   true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := checkHostAccess("kube-prometheus-stack", test.namespace, []byte(test.manifests))
			if !test.wantErr {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)
			// The operator has to be able to act on it: what was asked for,
			// and where.
			assert.Contains(t, err.Error(), "hostNetwork")
			assert.Contains(t, err.Error(), "hostPID")
			assert.Contains(t, err.Error(), "hostPath")
			assert.Contains(t, err.Error(), "observability")
		})
	}
}

func TestDocumentNamespace(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		doc  string
		want string
	}{
		{
			name: "reads metadata.namespace",
			doc:  "metadata:\n  name: x\n  namespace: kube-system\n",
			want: "kube-system",
		},
		{
			name: "unquotes a quoted namespace",
			doc:  "metadata:\n  namespace: \"kube-system\"\n",
			want: "kube-system",
		},
		{
			// Cluster-scoped objects have none, and Helm resolves them
			// against the release.
			name: "falls back when the document names none",
			doc:  "kind: ClusterRole\nmetadata:\n  name: x\n",
			want: "observability",
		},
		{
			// `namespace: ""` is not a namespace called "": Helm resolves an
			// empty value against the release, and reporting "" told the
			// operator nothing about where the workload was going.
			name: "falls back when the key is empty",
			doc:  "metadata:\n  namespace:\n  name: x\n",
			want: "observability",
		},
		{
			// Not the `namespace:` inside a subject list or a field selector:
			// metadata sits at two spaces and a rendered chart is machine-
			// written.
			name: "ignores a namespace at another indentation",
			doc:  "subjects:\n  - kind: ServiceAccount\n    namespace: kube-system\n",
			want: "observability",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, test.want, documentNamespace(test.doc, "observability"))
		})
	}
}

func TestParseWorkloads_TellsNamespacesApart(t *testing.T) {
	t.Parallel()

	// The whole node-exporter incident was a namespace: the DaemonSet was
	// rendered into observability, where Pod Security refuses it, and the
	// check printed `ok kube-system/...` because it compared only kind and
	// name. A gate that reports a namespace it never looked at is worse than
	// one that says nothing.
	manifest := []byte(`---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: node-exporter
  namespace: observability
spec:
`)

	found := parseWorkloads(manifest, "observability")

	assert.True(t, found["DaemonSet/observability/node-exporter"])
	assert.False(t, found["DaemonSet/kube-system/node-exporter"],
		"a workload rendered into the wrong namespace must not match")
}

func TestParseWorkloads_IgnoresAKindThatIsNotTheDocuments(t *testing.T) {
	t.Parallel()

	// An autoscaler names the workload it scales, and a RoleBinding names its
	// subjects. Reading either as the document's own kind attributes a
	// workload to whatever refers to it — the object counted would be one
	// nothing renders.
	manifest := []byte(`---
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: web
  namespace: default
spec:
  scaleTargetRef:
    kind: Deployment
    name: web
`)

	assert.Empty(t, parseWorkloads(manifest, "default"),
		"only a top-level kind describes the document")
}

func TestHelmArgs_TellHelmWhatTheClusterServes(t *testing.T) {
	t.Parallel()

	// An offline render cannot ask a cluster which APIs it has, and helm only
	// populates .Capabilities.APIVersions from a small built-in list. A chart
	// testing `.Capabilities.APIVersions.Has "policy/v1/PodDisruptionBudget"`
	// then takes its fallback branch and emits policy/v1beta1 — removed in
	// Kubernetes 1.25, so the pinned cluster would reject it.
	//
	// Measured, not hypothetical: that is exactly what Traefik rendered the
	// first time this check saw the layer's own values.
	chart, err := charts.Get("traefik")
	require.NoError(t, err)

	args := helmArgs(chart, "traefik", "traefik", nil)

	assert.Contains(t, args, "--api-versions")
	assert.Contains(t, args, "policy/v1/PodDisruptionBudget")
	assert.Contains(t, args, "--kube-version")
	assert.Contains(t, args, pinnedKubernetesVersion(),
		"the render must use the Kubernetes version the topology pins")
}
