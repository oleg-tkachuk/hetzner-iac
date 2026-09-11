// `render` proves, offline, that the pinned charts still produce the workloads
// pkg/workloads expects.
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
	"embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/charts"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/chartsettings"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/hetzner"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/workloads"
)

// values/ holds the minimum each chart needs to render the topology this
// platform actually deploys. Charts whose default topology already matches
// have no file here.
//
// --set was not enough: Loki needs a nested schemaConfig list, which the flag
// form cannot express, and the chart refuses to render without it.
//
//go:embed values
var valuesFS embed.FS

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

		found, err := renderChart(ctx, chart, expected[0].Release, expected[0].Namespace, key)
		if err != nil {
			return 0, fmt.Errorf("render %s: %w", key, err)
		}

		for _, want := range expected {
			label := fmt.Sprintf("%s %s/%s", want.Kind, want.Namespace, want.Name)

			if found[string(want.Kind)+"/"+want.Name] {
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

	for _, effect := range chartsettings.Effects {
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
// keyed "Kind/name".
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

	return parseWorkloads(output), nil
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
//     resources kube-prometheus-stack installs the definitions for in the same
//     release, so nothing can validate them at render time. Collected by
//     running kubeconform over every chart and reading what it could not
//     resolve, rather than one failure per iteration.
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
	return strings.TrimPrefix(hetzner.DefaultKubernetesVersion, "v")
}

// renderRaw templates a chart and returns its manifests.
func renderRaw(ctx context.Context, chart charts.Chart, release, namespace, key string, sets ...string) ([]byte, error) {
	args := []string{
		"template", release, chart.Name,
		"--repo", chart.Repo,
		"--version", chart.Version,
		"--namespace", namespace,
		"--skip-tests",
	}

	for _, set := range sets {
		args = append(args, "--set", set)
	}

	valuesFile, err := writeValues(key)
	if err != nil {
		return nil, err
	}

	if valuesFile != "" {
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

// parseWorkloads pulls "Kind/name" out of rendered YAML.
//
// A line-scanner rather than a YAML parse: the output is a multi-document
// stream containing CRDs whose schemas are large, and only the first
// `name:` after a workload `kind:` is needed.
func parseWorkloads(output []byte) map[string]bool {
	found := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<24)

	kind := ""

	for scanner.Scan() {
		line := scanner.Text()

		switch strings.TrimSpace(line) {
		case "kind: Deployment", "kind: StatefulSet", "kind: DaemonSet":
			kind = strings.TrimPrefix(strings.TrimSpace(line), "kind: ")

			continue
		}

		// Only a top-level metadata name counts; a name nested deeper belongs
		// to a container or a volume.
		if kind != "" && strings.HasPrefix(line, "  name: ") {
			found[kind+"/"+strings.TrimSpace(strings.TrimPrefix(line, "  name:"))] = true
			kind = ""
		}
	}

	return found
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	sort.Strings(out)

	return out
}

// writeValues materialises the embedded values file for a chart, if it has
// one, and returns its path. An empty path means the chart renders correctly
// with its defaults.
func writeValues(key string) (string, error) {
	content, err := valuesFS.ReadFile(filepath.Join("values", key+".yaml"))
	if err != nil {
		return "", nil //nolint:nilerr // no file simply means no overrides
	}

	file, err := os.CreateTemp("", key+"-values-*.yaml")
	if err != nil {
		return "", fmt.Errorf("temp values file: %w", err)
	}

	if _, err := file.Write(content); err != nil {
		_ = file.Close()

		return "", fmt.Errorf("write values: %w", err)
	}

	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close values: %w", err)
	}

	return file.Name(), nil
}
