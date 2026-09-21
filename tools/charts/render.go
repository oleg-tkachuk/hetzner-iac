// `render` proves, offline, that the pinned charts still produce the workloads
// internal/pkg/workloads expects.
//
// Part of this tool rather than its own, because its subject is the charts:
// two commands named after the same noun is the signal they are one.
//
// It exists because the alternative is finding out on a real cluster. A chart
// upgrade that renames a Deployment does not fail `pulumi up` — the release
// installs, and the e2e suite then fails hours later against production. This
// runs `helm template` against the pinned versions and compares, in seconds,
// with no cluster and no credentials.
//
// It renders with --repo rather than `helm repo add`, so a read-only check
// does not mutate the operator's Helm configuration as a side effect.
//
// The effect pass was verified by breaking it on purpose: shortening
// kubeProxyReplacement by one letter turns the check red with
//
//	MISS  cilium  kubeProxyReplacment=true produced no `kube-proxy-replacement: "true"`
//
// while `helm template` itself renders that typo without complaint and exits
// zero. A gate nobody has seen fail is a gate nobody knows works.
package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/charts"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/workloads"

	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

// renderAll runs every chart check and reports how many failed.
func renderAll() error {
	failures, err := run()
	if err != nil {
		return err
	}

	if failures > 0 {
		return fmt.Errorf(
			"%d check(s) failed — a chart renamed something, or a value it used to read is now ignored",
			failures)
	}

	return nil
}

func run() (int, error) {
	// A registry that stops responding must not hang the check forever.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if _, err := exec.LookPath("helm"); err != nil {
		return 0, fmt.Errorf("helm is not installed: %w", err)
	}

	failures := 0

	for _, key := range workloads.Charts() {
		chart, err := charts.Get(key)
		if err != nil {
			return 0, err
		}

		expected := rendered(key)
		if len(expected) == 0 {
			continue // every workload of this chart is operator-created
		}

		// The namespace comes from the registry, which is what the layer
		// installs the release into. Taking it from the first expected
		// workload made the render depend on the order of a table, because a
		// chart can install into two namespaces — one workload needing host
		// access goes to kube-system while the rest stay in the release's own
		// — and whichever entry came first decided what `helm template -n`
		// was given.
		found, err := renderChart(ctx, chart, expected[0].Release, chart.Namespace, key)
		if err != nil {
			return 0, fmt.Errorf("render %s: %w", key, err)
		}

		for _, want := range expected {
			label := fmt.Sprintf("%s %s/%s", want.Kind, want.Namespace, want.Name)

			if found[string(want.Kind)+"/"+want.Namespace+"/"+want.Name] {
				fmt.Printf("ok    %-14s %s\n", key, label)

				continue
			}

			if want.Optional {
				fmt.Printf("skip  %-14s %s (optional, not rendered)\n", key, label)

				continue
			}

			fmt.Printf("MISS  %-14s %s\n", key, label)

			failures++

			if names := sortedKeys(found); len(names) > 0 {
				fmt.Printf("      chart rendered: %s\n", strings.Join(names, ", "))
			}
		}
	}

	failures += checkEffects(ctx)
	failures += checkResources(ctx)

	return failures, nil
}

// checkEffects verifies that the settings whose misspelling fails silently
// actually reach the chart's output.
//
// Rendering with the value is not enough on its own — Helm accepts any key,
// including one no template reads. The assertion is on the EFFECT: the line
// the chart's own template produces. A key the chart ignores cannot satisfy it.
func checkEffects(ctx context.Context) int {
	failures := 0

	for _, effect := range charts.Effects() {
		chart, err := charts.Get(effect.Chart)
		if err != nil {
			fmt.Printf("MISS  %-14s unknown chart\n", effect.Chart)

			failures++

			continue
		}

		output, err := renderRaw(ctx, chart, effect.Release, effect.Namespace, effect.Chart, effect.Set...)
		if err != nil {
			fmt.Printf("MISS  %-14s render failed: %v\n", effect.Chart, err)

			failures++

			continue
		}

		if strings.Contains(string(output), effect.Expect) {
			fmt.Printf("ok    %-14s %s\n", effect.Chart, strings.Join(effect.Set, " "))

			continue
		}

		fmt.Printf("MISS  %-14s %s produced no %q\n", effect.Chart, strings.Join(effect.Set, " "), effect.Expect)
		fmt.Printf("      %s\n", effect.Why)

		failures++
	}

	return failures
}

// rendered returns the workloads of one chart that helm template can show.
func rendered(key string) []workloads.Workload {
	var out []workloads.Workload

	for _, w := range workloads.ForChart(key) {
		if !w.OperatorCreated {
			out = append(out, w)
		}
	}

	return out
}

// renderChart templates a chart and returns the workload objects it produced,
// keyed "Kind/namespace/name".
func renderChart(ctx context.Context, chart charts.Chart, release, namespace, key string) (map[string]bool, error) {
	output, err := renderRaw(ctx, chart, release, namespace, key)
	if err != nil {
		return nil, err
	}

	// Validated against the Kubernetes version the topology pins, not just
	// parsed for workload names.
	//
	// parseWorkloads answers "does this chart still produce the Deployment we
	// expect". It says nothing about whether that Deployment's apiVersion
	// still exists. This repository has already lost a control plane to that
	// class once: Kubernetes v1.36 removed a kube-apiserver flag the machine
	// config was passing. A chart emitting a removed API version is the same
	// failure with a different name, and it is found at apply, halfway
	// through.
	if err := validateSchema(ctx, key, output); err != nil {
		return nil, err
	}

	if err := checkHostAccess(key, namespace, output); err != nil {
		return nil, err
	}

	if err := checkStorageClasses(key, output); err != nil {
		return nil, err
	}

	return parseWorkloads(output, namespace)
}

// hostAccessMarkers are the pod-spec fields Pod Security Admission's baseline
// level forbids. Matched on the rendered text rather than by unmarshalling
// every manifest: the question is only whether a chart asks for host access at
// all, and a false positive here is a comment away from being explained.
var hostAccessMarkers = []string{
	"hostNetwork: true",
	"hostPID: true",
	"hostIPC: true",
	"hostPort:",
	"hostPath:",
}

// checkHostAccess refuses a chart that needs host access in a namespace Talos
// does not exempt from Pod Security Admission.
//
// Per document, not per release: one chart can install into two namespaces —
// a node exporter into kube-system because it needs host access, the rest into
// the release's own namespace under baseline. Checking the release's namespace
// would flag the whole chart for what one DaemonSet asks. No chart pinned here
// does that today; the check is per document because the next one will.
//
// The failure this replaces gave almost nothing to go on: the DaemonSet showed
// DESIRED 1, CURRENT 0 — not a pending pod, no pod at all — and Helm then
// waited out its whole timeout while every other workload in the release was
// Ready. The only evidence was one event on the DaemonSet.
func checkHostAccess(key, releaseNamespace string, manifests []byte) error {
	docs, err := documents(manifests)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}

	for _, doc := range docs {
		// A schema is not a pod. Skipped before the markers are looked for,
		// because they are certainly there: see SchemaKind.
		if doc.Kind == SchemaKind {
			continue
		}

		needs := hostAccessIn(doc.Text)
		if len(needs) == 0 {
			continue
		}

		namespace := doc.NamespaceOr(releaseNamespace)
		if slices.Contains(clusterspec.PodSecurityExemptNamespaces, namespace) {
			continue
		}

		return fmt.Errorf(
			"%s asks for host access (%s) in namespace %q, which Talos does not exempt "+
				"from Pod Security Admission.\nIts pods will not be created at all — the "+
				"workload reports zero replicas and Helm waits out its timeout.\n"+
				"Either install it into one of %v, or give it a namespace labelled "+
				"pod-security.kubernetes.io/enforce=privileged",
			key, strings.Join(needs, ", "), namespace, clusterspec.PodSecurityExemptNamespaces)
	}

	return nil
}

// hostAccessIn returns the host-access markers present in one document.
func hostAccessIn(doc string) []string {
	var found []string

	for _, marker := range hostAccessMarkers {
		if strings.Contains(doc, marker) {
			found = append(found, strings.TrimSuffix(strings.TrimSuffix(marker, ": true"), ":"))
		}
	}

	return found
}

// SchemaKind is the one kind whose text names host access with no pod asking
// for it.
//
// A CustomResourceDefinition carries an OpenAPI schema, and a CRD with a pod
// template in it carries the whole PodSpec schema — so `hostPath` and
// `hostPort` appear as PROPERTY NAMES in a document that creates nothing.
// KEDA's ScaledJob is the first chart pinned here to do that, and the check
// reported the entire chart as asking for host access in a namespace Talos
// does not exempt. This is the case render.go's own comment predicted when it
// said the check is per document "because the next one will".
const SchemaKind = "CustomResourceDefinition"

// isSchemaDocument answers whether a document is a SchemaKind.
//
// Its own reader rather than documentKind next door, which deliberately
// answers "" for anything that is not a workload and so cannot tell a CRD from
// a Service.
//
// Answered from the parsed object rather than by finding a `kind:` line, which
// is what this file used to do everywhere and defended as "not a YAML parser
// and does not need to be". It did need to be — see documents below.

// objectFields is what one rendered document is read into.
//
// json tags, because sigs.k8s.io/yaml reads YAML by converting it to JSON:
// that is how Kubernetes' own clients unmarshal a manifest, so every field
// here is spelled exactly as the API spells it.
//
// Only the fields this tool asks about. An unknown key is ignored, which is
// the whole point — a CRD's schema can be thousands of lines and none of them
// are a question anybody here is asking.
type objectFields struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	// ReclaimPolicy is a StorageClass's, and it sits at the top level of the
	// object rather than under a spec.
	ReclaimPolicy string `json:"reclaimPolicy"`
}

// document is one rendered object: its fields, and its text.
//
// The text is kept because two checks ask whether a MARKER appears anywhere in
// a document rather than what a named field holds — see hostAccessIn, which is
// deliberately over-broad because the shapes it looks through are four
// different workload kinds' pod templates.
type document struct {
	Text string

	Kind          string
	Name          string
	Namespace     string
	ReclaimPolicy string
}

// NamespaceOr is metadata.namespace, or the release's when the object leaves
// it out.
//
// An empty value is not a namespace named "": Helm resolves it against the
// release, the same as an absent key. Returning "" instead reported
// `in namespace ""`, which tells the operator nothing about where the workload
// was actually going.
func (d document) NamespaceOr(fallback string) string {
	if d.Namespace != "" {
		return d.Namespace
	}

	return fallback
}

// documents splits a rendered manifest stream and reads every object in it.
//
// Both halves are Kubernetes' own libraries, which is the point.
// apimachinery's YAMLReader is the splitter kubectl uses, and
// sigs.k8s.io/yaml the reader its clients use — so a document is separated and
// interpreted here exactly as it would be by whatever applies it.
//
// What this replaces was `strings.Split(manifests, "\n---")` feeding three
// line scanners, each carrying a comment saying a YAML parser was not needed.
//
// The split itself was sound, and worth saying so rather than inventing a
// fault for it: cutting at a newline followed by `---` needs a line at column
// zero to go wrong, and a mapping's own content is always indented, so
// well-formed output cannot produce one.
//
// The field reads were not. Each matched on INDENTATION — the object's name
// was the first `  name:` in the text, and a StorageClass's was the first
// `name:` at ANY depth — so a document whose two-space `name` belongs to
// something other than metadata answered with that instead, and the check then
// compared against a workload nobody had rendered. Reading the object is the
// same question without the guess.
//
// It is also less code, and the CRD-schema cost the old comment worried about
// is a few milliseconds over the twelve pinned charts.
func documents(manifests []byte) ([]document, error) {
	reader := k8syaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(manifests)))

	var out []document

	for {
		chunk, err := reader.Read()
		if errors.Is(err, io.EOF) {
			return out, nil
		}

		if err != nil {
			return nil, fmt.Errorf("split the rendered manifests: %w", err)
		}

		if strings.TrimSpace(string(chunk)) == "" {
			continue
		}

		var fields objectFields
		if err := yaml.Unmarshal(chunk, &fields); err != nil {
			return nil, fmt.Errorf("read a rendered document: %w", err)
		}

		out = append(out, document{
			Text:          string(chunk),
			Kind:          fields.Kind,
			Name:          fields.Metadata.Name,
			Namespace:     fields.Metadata.Namespace,
			ReclaimPolicy: fields.ReclaimPolicy,
		})
	}
}

// kubeconformBinary is looked up rather than assumed so the absence is one
// clear message instead of an exec error.
const kubeconformBinary = "kubeconform"

// skippedKinds are the kinds with no schema to validate against, named one by
// one on purpose.
//
//   - CustomResourceDefinition: upstream publishes no CRD schema in the strict
//     standalone set.
//   - Alertmanager, Prometheus, PrometheusRule, ServiceMonitor: custom
//     resources whose definitions arrive with the chart that renders them, so
//     nothing can validate them at render time. Nothing pinned here emits
//     them since the observability layer left, and they stay listed because a
//     chart that does is one Argo CD application away. Collected by running
//     kubeconform over every chart and reading what it could not resolve,
//     rather than one failure per iteration.
//
// A new kind here is a deliberate edit, and that is the point: the flag that
// would make this list unnecessary, -ignore-missing-schemas, also skips a
// REMOVED api version, which is the failure this whole check exists for.
var skippedKinds = []string{
	"CustomResourceDefinition",
	"Alertmanager",
	"Prometheus",
	"PrometheusRule",
	"ServiceMonitor",
}

// validateSchema checks rendered manifests against the pinned Kubernetes
// version's schemas.
//
// Only the CustomResourceDefinition kind is skipped, and that distinction is
// the whole value of this check. The obvious flag, -ignore-missing-schemas,
// makes it worthless: a removed API has no schema either, so
// `policy/v1beta1 PodSecurityPolicy` came back "Skipped" and the check passed
// — measured, which is the only reason it is not still written that way.
//
// Skipping just the CRD kind leaves every built-in validated: a removed API
// version fails, and so does a misspelled field. CRDs have no schema in the
// strict standalone set upstream publishes, which is why that one is named.
//
// Not offline: kubeconform fetches the schemas for the pinned version. That is
// a network dependency this check adds, and worth knowing before it fails in a
// place with no egress.
func validateSchema(ctx context.Context, key string, manifests []byte) error {
	if _, err := exec.LookPath(kubeconformBinary); err != nil {
		return fmt.Errorf("kubeconform is not installed (brew bundle, or brew install kubeconform): %w", err)
	}

	version := pinnedKubernetesVersion()

	// #nosec G204 -- every argument comes from kubeconformArgs, which builds
	// them from literals in this file plus a version from a package constant.
	// CommandContext takes a vector, so no shell reads any of it.
	cmd := exec.CommandContext(ctx, kubeconformBinary, kubeconformArgs(version)...)
	cmd.Stdin = bytes.NewReader(manifests)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s does not validate against Kubernetes %s:\n%s",
			key, version, strings.TrimSpace(string(out)))
	}

	return nil
}

// kubeconformArgs builds the invocation, separated so a test can assert what
// is and is not in it.
func kubeconformArgs(version string) []string {
	return []string{
		"-strict",
		"-kubernetes-version", version,
		"-skip", strings.Join(skippedKinds, ","),
		"-summary",
		"-",
	}
}

// pinnedKubernetesVersion is the version the schemas are checked against.
//
// The package constant rather than the committed topology file: a relative
// path resolves differently depending on where `go run` was invoked from, and
// the two cannot disagree anyway — TestEveryTopologyPresentPinsKubernetes
// asserts every topology states exactly this value.
func pinnedKubernetesVersion() string {
	// kubeconform wants 1.36.4, the constant says v1.36.4.
	return strings.TrimPrefix(clusterspec.DefaultKubernetesVersion, "v")
}

// clusterAPIVersions are the API versions a chart may branch on, told to
// helm because an offline render cannot ask a cluster.
//
// `helm template` populates .Capabilities.APIVersions from a small built-in
// list, and --kube-version only sets .Capabilities.KubeVersion — so a chart
// testing `.Capabilities.APIVersions.Has "policy/v1/PodDisruptionBudget"`
// takes its fallback branch and emits policy/v1beta1, removed in Kubernetes
// 1.25. That is what this check found the first time it rendered Traefik with
// the layer's own values: a PodDisruptionBudget the pinned cluster would
// reject, produced only by rendering offline.
//
// Each entry is an api this platform's Kubernetes serves. Adding one is
// telling the render the truth about the cluster, not relaxing a check.
var clusterAPIVersions = []string{
	"policy/v1/PodDisruptionBudget",
}

// helmArgs is the invocation, separated so a test can assert what the render
// tells helm about the cluster without running it.
func helmArgs(chart charts.Chart, release, namespace string, sets []string) []string {
	args := []string{
		"template", release, chart.Name,
		"--repo", chart.Repo,
		"--version", chart.Version,
		"--namespace", namespace,
		"--skip-tests",
		"--kube-version", pinnedKubernetesVersion(),
	}

	for _, api := range clusterAPIVersions {
		args = append(args, "--api-versions", api)
	}

	for _, set := range sets {
		args = append(args, "--set", set)
	}

	return args
}

// renderRaw templates a chart and returns its manifests.
func renderRaw(ctx context.Context, chart charts.Chart, release, namespace, key string, sets ...string) ([]byte, error) {
	args := helmArgs(chart, release, namespace, sets)

	valuesFile, err := writeValues(key)
	if err != nil {
		return nil, err
	}

	if valuesFile != "" {
		// A temp file this function made. Failing to remove it leaves a file in
		// the OS temp directory, which is not worth failing a render over.
		defer func() { _ = os.Remove(valuesFile) }()

		args = append(args, "--values", valuesFile)
	}

	// #nosec G204 -- a fixed argv built from the pinned chart registry; no shell is involved.
	output, err := exec.CommandContext(ctx, "helm", args...).Output()
	if err != nil {
		// helm writes the useful part — the template error and its line — to
		// stderr, which Output() captures only on an ExitError.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("helm template failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}

		return nil, err
	}

	return output, nil
}

// parseWorkloads pulls "Kind/namespace/name" out of rendered YAML.
//
// Per document rather than a running `kind` across lines, because the
// namespace belongs to a document and a line scanner cannot say which
// document it is inside. It used to key on kind and name alone, which made
// the namespace in its own output a claim it had never checked: a workload
// rendered into the wrong namespace matched, and the `ok` line printed the
// namespace that was expected rather than the one the chart produced. That is
// exactly what the node-exporter deploys hit twice.
//
// It reports an unreadable stream rather than returning what it managed to
// find. The input is `helm template`'s own output, so a document this cannot
// read is a broken tool rather than a broken chart — and a check that silently
// parsed half a stream would report a missing workload as the chart's fault.
func parseWorkloads(output []byte, releaseNamespace string) (map[string]bool, error) {
	docs, err := documents(output)
	if err != nil {
		return nil, err
	}

	found := map[string]bool{}

	for _, doc := range docs {
		// Only the kinds this check tracks. Counting a Service or a CRD would
		// make it pass on a chart that renders no workloads at all.
		if !slices.Contains(workloadKinds, doc.Kind) {
			continue
		}

		if doc.Name == "" {
			continue
		}

		found[doc.Kind+"/"+doc.NamespaceOr(releaseNamespace)+"/"+doc.Name] = true
	}

	return found, nil
}

// workloadKinds are the kinds this check tracks, spelled as Kubernetes spells
// them. Nothing here runs as a Job: Helm hook Jobs exist but finish, so they
// are not something to assert is healthy.
var workloadKinds = []string{
	string(workloads.Deployment),
	string(workloads.StatefulSet),
	string(workloads.DaemonSet),
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	slices.Sort(out)

	return out
}

// writeValues materialises the embedded values file for a chart, if it has
// one, and returns its path. An empty path means the chart renders correctly
// with its defaults.
func writeValues(key string) (string, error) {
	probe, err := charts.Probe(key)
	if err != nil {
		// A chart with no probe data renders on the chart's own defaults,
		// which is right for the charts this platform does not configure.
		return "", nil //nolint:nilerr // no template simply means no overrides
	}

	rendered, err := charts.Render(key, probe)
	if err != nil {
		return "", err
	}

	content := []byte(rendered)

	file, err := os.CreateTemp("", key+"-values-*.yaml")
	if err != nil {
		return "", fmt.Errorf("temp values file: %w", err)
	}

	if _, err := file.Write(content); err != nil {
		// The write error is the one worth reporting; a Close error on top of
		// it would replace a cause with a consequence. The SUCCESSFUL path
		// below checks Close, because there a failure means bytes never
		// reached the disk.
		_ = file.Close()

		return "", fmt.Errorf("write values: %w", err)
	}

	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close values: %w", err)
	}

	return file.Name(), nil
}

// StorageClassKind is the kind a class is declared as.
const StorageClassKind = "StorageClass"

// storageClassProvisioner is the chart whose classes this checks.
const storageClassProvisioner = "hcloud-csi"

// expectedStorageClasses is the reclaim policy each class must render with,
// and the check exists because a reclaim policy is invisible until it acts.
//
// A class is declared in one place and named by a claim in another, and the
// only observable difference between Delete and Retain is what happens when
// somebody deletes a PersistentVolumeClaim — by which time the answer is
// either "the volume is still there" or "the data is gone". Helm would also
// accept a `storageClasses` list with a key it does not know and keep its own
// default, silently, which is how the second class could exist in the values
// file and nowhere else.
var expectedStorageClasses = map[string]string{
	platform.StorageClass:         "Delete",
	platform.StorageClassDatabase: "Retain",
}

// checkStorageClasses holds the rendered classes to internal/pkg/platform.
func checkStorageClasses(key string, manifests []byte) error {
	if key != storageClassProvisioner {
		return nil
	}

	docs, err := documents(manifests)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}

	rendered := map[string]string{}

	for _, doc := range docs {
		// The object's own kind, not the text containing it: a CRD that
		// mentions StorageClass is not one, and the old `strings.Contains`
		// could not tell the difference.
		if doc.Kind != StorageClassKind {
			continue
		}

		if doc.Name == "" {
			continue
		}

		rendered[doc.Name] = doc.ReclaimPolicy
	}

	for name, policy := range expectedStorageClasses {
		got, declared := rendered[name]
		if !declared {
			return fmt.Errorf("%s renders no StorageClass %q: a claim naming it stays Pending, "+
				"and nothing on the workload says why", key, name)
		}

		if got != policy {
			return fmt.Errorf("%s renders StorageClass %q with reclaimPolicy %q, wanted %q: "+
				"the difference is only observable when a claim is deleted, and then it is "+
				"either a volume or an outage", key, name, got, policy)
		}
	}

	if len(rendered) != len(expectedStorageClasses) {
		return fmt.Errorf("%s renders %d storage classes, wanted %d: the values file REPLACES "+
			"the chart's list, so an extra one is a class nothing in this repository names",
			key, len(rendered), len(expectedStorageClasses))
	}

	return nil
}
