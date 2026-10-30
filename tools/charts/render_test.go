package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/charts"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/workloads"

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

	found, err := parseWorkloads(manifest, "kube-system")
	require.NoError(t, err)

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

	found, err := parseWorkloads(manifest, "kube-system")
	require.NoError(t, err)
	assert.Empty(t, found)
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

	found, err := parseWorkloads(manifest, "observability")
	require.NoError(t, err)

	require.Len(t, found, 1)
	assert.True(t, found["StatefulSet/observability/loki"])
}

func TestParseWorkloads_Empty(t *testing.T) {
	t.Parallel()

	for _, empty := range [][]byte{nil, []byte(""), []byte("---\n")} {
		found, err := parseWorkloads(empty, "kube-system")
		require.NoError(t, err)
		assert.Empty(t, found)
	}
}

func TestWriteValues(t *testing.T) {
	t.Parallel()

	// Traefik needs a nested proxyProtocol trust list per entry point, which
	// the --set form cannot express, so its file must exist and be non-empty.
	path, err := writeValues("traefik")
	require.NoError(t, err)
	require.NotEmpty(t, path)

	t.Cleanup(func() { _ = os.Remove(path) })

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(content), "proxyProtocol")
	assert.Contains(t, string(content), "trustedIPs")
}

func TestWriteValues_NoFileMeansNoOverrides(t *testing.T) {
	t.Parallel()

	// No values template means the chart renders on its own defaults, and that
	// is not an error — the caller passes the empty path to helm as "no -f".
	//
	// Asserted against a key with no template rather than a real chart: every
	// chart in the registry now has one, because every chart now carries
	// resource requests and limits. A test naming a real chart would have to
	// be rewritten the next time that changes, and the branch it covers is
	// about the absent file, not about which chart happens to lack one.
	path, err := writeValues("a-chart-with-no-values-template")
	require.NoError(t, err)
	assert.Empty(t, path)
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
	args := kubeconformArgs("1.37.1")

	assert.NotContains(t, args, "-ignore-missing-schemas",
		"a removed api version has no schema either, so this flag hides exactly what the check is for")
	assert.Contains(t, args, "-strict")
	assert.Contains(t, args, "1.37.1")
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

	// A CRD whose schema embeds a pod template, reduced the same way. Every
	// marker the gate looks for is in here, and none of them creates a pod.
	const scaledJobCRD = `
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: scaledjobs.keda.sh
spec:
  versions:
    - schema:
        openAPIV3Schema:
          properties:
            jobTargetRef:
              properties:
                template:
                  properties:
                    spec:
                      properties:
                        hostNetwork:
                          type: boolean
                        hostPID:
                          type: boolean
                        volumes:
                          items:
                            properties:
                              hostPath:
                                type: object
`

	tests := []struct {
		name      string
		manifests string
		namespace string
		wantErr   bool
	}{
		{
			// The case KEDA brought. Refusing this refuses the whole chart,
			// for a document that creates nothing at all.
			name:      "a schema that names host access is not a pod asking for it",
			manifests: scaledJobCRD,
			namespace: "keda",
		},
		{
			// A manifest carried as data is text, not a pod. Matching the
			// text refused a chart for a document that creates nothing.
			name: "a ConfigMap quoting a manifest is not a pod asking for host access",
			manifests: `
apiVersion: v1
kind: ConfigMap
metadata:
  name: example-dashboards
  namespace: observability
data:
  example.yaml: |
    volumes:
      - hostPath:
          path: /
    hostNetwork: true
`,
			namespace: "observability",
		},
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

func TestDocumentNamespaceOr(t *testing.T) {
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
			// Quoting is the YAML reader's problem now, and it was a
			// strings.Trim of `"'` before.
			name: "a quoted namespace is the same namespace",
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
			// Not the `namespace:` inside a subject list. It used to be
			// excluded by its indentation; now it is excluded because it is
			// not metadata.namespace, which is the actual reason.
			name: "a namespace belonging to something else is not the object's",
			doc:  "subjects:\n  - kind: ServiceAccount\n    namespace: kube-system\n",
			want: "observability",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			docs, err := documents([]byte(test.doc))
			require.NoError(t, err)
			require.Len(t, docs, 1)

			assert.Equal(t, test.want, docs[0].NamespaceOr("observability"))
		})
	}
}

// TestDocuments_ReadTheObjectsOwnFields covers what the line scanners got
// wrong, which is the reason they are gone.
//
// Both documents here are valid YAML that the old readers answered incorrectly:
// the name was the first `  name:` in the text, and a StorageClass's was the
// first `name:` at any depth at all.
func TestDocuments_ReadTheObjectsOwnFields(t *testing.T) {
	t.Parallel()

	t.Run("a two-space name that is not metadata's", func(t *testing.T) {
		t.Parallel()

		docs, err := documents([]byte(`kind: Deployment
spec:
  name: not-the-object
metadata:
  name: the-object
  namespace: kube-system
`))
		require.NoError(t, err)
		require.Len(t, docs, 1)

		assert.Equal(t, "the-object", docs[0].Name,
			"the first two-space `name:` in the text is not the object's name")
	})

	t.Run("a storage class whose annotations come first", func(t *testing.T) {
		t.Parallel()

		docs, err := documents([]byte(`kind: StorageClass
metadata:
  annotations:
    name: an-annotation-called-name
  name: hcloud-volumes
reclaimPolicy: Delete
`))
		require.NoError(t, err)
		require.Len(t, docs, 1)

		assert.Equal(t, "hcloud-volumes", docs[0].Name,
			"the first `name:` at any depth is not the object's name")
		assert.Equal(t, "Delete", docs[0].ReclaimPolicy)
	})

	t.Run("a multi-document stream is split by the reader", func(t *testing.T) {
		t.Parallel()

		docs, err := documents([]byte(`# Source: chart/templates/a.yaml
kind: Deployment
metadata:
  name: a
---
# Source: chart/templates/b.yaml
kind: DaemonSet
metadata:
  name: b
`))
		require.NoError(t, err)
		require.Len(t, docs, 2)

		assert.Equal(t, "Deployment", docs[0].Kind)
		assert.Equal(t, "DaemonSet", docs[1].Kind)
	})
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

	found, err := parseWorkloads(manifest, "observability")
	require.NoError(t, err)

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

	found, err := parseWorkloads(manifest, "default")
	require.NoError(t, err)

	assert.Empty(t, found, "only a top-level kind describes the document")
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

// The class check runs for the CSI chart's registry key. Keyed by a literal,
// a renamed key made it skip every chart and pass having read nothing.
func TestCheckStorageClasses_RunsForTheCSIChart(t *testing.T) {
	t.Parallel()

	noClasses := []byte("apiVersion: v1\nkind: ConfigMap\nmetadata: {name: x}\n")

	err := checkStorageClasses(charts.HcloudCSI, noClasses)
	require.Error(t, err, "the CSI chart rendering no class must fail")
	assert.Contains(t, err.Error(), "renders no StorageClass")

	assert.NoError(t, checkStorageClasses(charts.Cilium, noClasses), "other charts declare no classes")
}

// errFixture is the error a fixture render fails with.
var errFixture = errors.New("boom")

// renderFixture stands in for helm: a chart renders its manifest from
// manifests, empty when it has none, and a chart in failing fails.
//
// Keyed by the values key, or by the release for a render on the chart's
// defaults, which names no values key and is released under the registry key.
type renderFixture struct {
	manifests map[string]string
	failing   map[string]bool
}

func (f renderFixture) render(_ context.Context, _ charts.Chart, release, _, key string, _ ...string) ([]byte, error) {
	if key == "" {
		key = release
	}

	if f.failing[key] {
		return nil, errFixture
	}

	return []byte(f.manifests[key]), nil
}

// podManifest is a Deployment running one container of image, labelled with
// its name.
func podManifest(name, image string) string {
	return `apiVersion: apps/v1
kind: Deployment
metadata: {name: ` + name + `}
spec:
  template:
    metadata: {labels: {app.kubernetes.io/name: ` + name + `}}
    spec:
      containers: [{name: ` + name + `, image: "` + image + `"}]
`
}

// missLines counts the lines a report marks as failed.
func missLines(out string) int {
	count := 0

	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, "MISS") {
			count++
		}
	}

	return count
}

func TestVerdict(t *testing.T) {
	t.Parallel()

	require.NoError(t, verdict(0, nil))
	require.ErrorIs(t, verdict(0, errFixture), errFixture, "a run that could not finish says why")

	err := verdict(3, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "3 check(s) failed")
}

// rendered keeps what helm template can show: everything but what an
// operator creates later.
func TestRendered_DropsOperatorCreatedWorkloads(t *testing.T) {
	t.Parallel()

	for _, key := range workloads.Charts() {
		var want []workloads.Workload

		for _, w := range workloads.Rendered() {
			if w.Chart == key {
				want = append(want, w)
			}
		}

		assert.Equal(t, want, rendered(key), key)
	}
}

func TestReportWorkloads(t *testing.T) {
	t.Parallel()

	expected := []workloads.Workload{
		{Kind: workloads.Deployment, Namespace: "traefik", Name: "traefik"},
		{Kind: workloads.DaemonSet, Namespace: "traefik", Name: "agent", Optional: true},
		{Kind: workloads.StatefulSet, Namespace: "traefik", Name: "store"},
	}

	var out bytes.Buffer

	failures := reportWorkloads(&out, "traefik", expected, map[string]bool{"Deployment/traefik/traefik": true})

	assert.Equal(t, 1, failures, "an optional workload missing is not a failure")
	assert.Equal(t, "ok    traefik        Deployment traefik/traefik\n"+
		"skip  traefik        DaemonSet traefik/agent (optional, not rendered)\n"+
		"MISS  traefik        StatefulSet traefik/store\n"+
		"      chart rendered: Deployment/traefik/traefik\n", out.String())

	out.Reset()

	assert.Equal(t, 1, reportWorkloads(&out, "traefik", expected[2:], map[string]bool{}))
	assert.NotContains(t, out.String(), "chart rendered", "nothing rendered is nothing to list")
}

// foundAll renders every workload the table expects of a chart.
func foundAll(_ context.Context, _ charts.Chart, _, _, key string) (map[string]bool, error) {
	found := map[string]bool{}
	for _, w := range workloads.ForChart(key) {
		found[string(w.Kind)+"/"+w.Namespace+"/"+w.Name] = true
	}

	return found, nil
}

func TestCheckWorkloads(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	failures, err := checkWorkloads(t.Context(), &out, foundAll)
	require.NoError(t, err)
	assert.Zero(t, failures)
	assert.Equal(t, len(workloads.Rendered()), strings.Count(out.String(), "ok    "),
		"one line per workload helm template shows")

	none := func(context.Context, charts.Chart, string, string, string) (map[string]bool, error) {
		return map[string]bool{}, nil
	}

	failures, err = checkWorkloads(t.Context(), &out, none)
	require.NoError(t, err)
	assert.Positive(t, failures)

	failing := func(context.Context, charts.Chart, string, string, string) (map[string]bool, error) {
		return nil, errFixture
	}

	_, err = checkWorkloads(t.Context(), &out, failing)
	require.ErrorIs(t, err, errFixture)
	assert.Contains(t, err.Error(), "render "+workloads.Charts()[0]+": ")
}

// Every failure the run counts is a line the operator can read, and a render
// that stops the run stops it.
func TestRunChecks_CountsWhatItPrints(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	failures, err := runChecks(t.Context(), &out, foundAll, renderFixture{}.render)
	require.NoError(t, err)
	assert.Positive(t, failures, "empty renders leave every effect and pin unproven")
	assert.Equal(t, missLines(out.String()), failures)

	failing := func(context.Context, charts.Chart, string, string, string) (map[string]bool, error) {
		return nil, errFixture
	}

	_, err = runChecks(t.Context(), &out, failing, renderFixture{}.render)
	require.ErrorIs(t, err, errFixture)
}

func TestCheckEffects(t *testing.T) {
	t.Parallel()

	render := renderFixture{
		manifests: map[string]string{charts.Cilium: "kube-proxy-replacement: \"true\"\n"},
		failing:   map[string]bool{charts.HcloudCCM: true},
	}.render

	var out bytes.Buffer

	failures := checkEffects(t.Context(), &out, render, []charts.Effect{
		{Chart: charts.Cilium, Expect: `kube-proxy-replacement: "true"`},
		{Chart: charts.Traefik, Expect: "proxyProtocol", Why: "the client address is lost"},
		{Chart: charts.HcloudCCM, Expect: "x"},
		{Chart: "no-such-chart", Expect: "x"},
	})

	assert.Equal(t, 3, failures)
	assert.Equal(t, "ok    cilium         kube-proxy-replacement: \"true\"\n"+
		"MISS  traefik        values template produced no \"proxyProtocol\"\n"+
		"      the client address is lost\n"+
		"MISS  hcloud-ccm     render failed: boom\n"+
		"MISS  no-such-chart  unknown chart\n", out.String())
}

func TestCheckedWorkloads(t *testing.T) {
	t.Parallel()

	valid := func(context.Context, string, []byte) error { return nil }
	deployment := []byte(podManifest("traefik", "traefik:v3"))

	found, err := checkedWorkloads(t.Context(), charts.Traefik, "traefik", deployment, valid)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"Deployment/traefik/traefik": true}, found)

	invalid := func(context.Context, string, []byte) error { return errFixture }
	_, err = checkedWorkloads(t.Context(), charts.Traefik, "traefik", deployment, invalid)
	require.ErrorIs(t, err, errFixture, "a render that does not validate goes no further")

	hostNetwork := []byte("apiVersion: apps/v1\nkind: DaemonSet\nmetadata: {name: x}\n" +
		"spec: {template: {spec: {hostNetwork: true}}}\n")
	_, err = checkedWorkloads(t.Context(), charts.Traefik, "traefik", hostNetwork, valid)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "asks for host access")

	_, err = checkedWorkloads(t.Context(), charts.HcloudCSI, "kube-system", deployment, valid)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "renders no StorageClass")

	_, err = checkedWorkloads(t.Context(), charts.HcloudCCM, "kube-system", []byte(ccmDeployment), valid)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "renders an environment variable twice")
}

func TestValidateWith(t *testing.T) {
	t.Parallel()

	manifests := []byte(podManifest("traefik", "traefik:v3"))

	require.NoError(t, validateWith(t.Context(), "true", charts.Traefik, manifests))

	err := validateWith(t.Context(), "false", charts.Traefik, manifests)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "traefik does not validate against Kubernetes "+pinnedKubernetesVersion())

	err = validateWith(t.Context(), "no-such-kubeconform", charts.Traefik, manifests)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kubeconform is not installed")
}

func TestHelmOutput(t *testing.T) {
	t.Parallel()

	out, err := helmOutput(t.Context(), "sh", []string{"-c", "printf rendered"})
	require.NoError(t, err)
	assert.Equal(t, "rendered", string(out))

	_, err = helmOutput(t.Context(), "sh", []string{"-c", "printf ' line 3: bad key \\n' >&2; exit 1"})
	require.Error(t, err)
	assert.Equal(t, "helm template failed: line 3: bad key", err.Error(), "helm's stderr is the useful part")

	_, err = helmOutput(t.Context(), "no-such-helm", nil)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "helm template failed", "a missing program is not a template error")
}

// echo prints the argv it was given, which is the invocation renderRaw makes.
func TestRenderWith_PassesTheValuesTemplateAndRemovesIt(t *testing.T) {
	t.Parallel()

	chart := charts.MustGet(charts.Traefik)

	out, err := renderWith(t.Context(), "echo", chart, "traefik", "traefik", charts.Traefik, "a=b")
	require.NoError(t, err)

	args := strings.Fields(string(out))
	assert.Equal(t, helmArgs(chart, "traefik", "traefik", []string{"a=b"}), args[:len(args)-2])
	assert.Equal(t, "--values", args[len(args)-2])

	_, statErr := os.Stat(args[len(args)-1])
	require.ErrorIs(t, statErr, os.ErrNotExist, "the values file is removed once helm has read it")

	out, err = renderWith(t.Context(), "echo", chart, "x", "x", "a-chart-with-no-values-template")
	require.NoError(t, err)
	assert.NotContains(t, string(out), "--values", "no template is no values file")
}

func TestCheckStorageClasses(t *testing.T) {
	t.Parallel()

	class := func(name, policy string) string {
		return "---\napiVersion: storage.k8s.io/v1\nkind: StorageClass\nmetadata: {name: " + name +
			"}\nreclaimPolicy: " + policy + "\n"
	}
	both := class(platform.StorageClass, "Delete") + class(platform.StorageClassDatabase, "Retain")

	require.NoError(t, checkStorageClasses(charts.HcloudCSI, []byte(both)))

	for name, tc := range map[string]struct {
		manifests string
		want      string
	}{
		"wrong policy": {
			class(platform.StorageClass, "Delete") + class(platform.StorageClassDatabase, "Delete"),
			`reclaimPolicy "Delete", wanted "Retain"`,
		},
		"an extra class": {both + class("extra", "Delete"), "renders 3 storage classes, wanted 2"},
		"unreadable":     {"kind: [unclosed", "hcloud-csi: "},
	} {
		err := checkStorageClasses(charts.HcloudCSI, []byte(tc.manifests))
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), tc.want, name)
	}

	unnamed := "apiVersion: storage.k8s.io/v1\nkind: StorageClass\nreclaimPolicy: Delete\n"
	classes, err := storageClassesIn([]byte(both + unnamed))
	require.NoError(t, err)
	assert.Len(t, classes, 2, "a class with no name is not one a claim can name")
}
