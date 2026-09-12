// Package charts is the single place every Helm chart version is pinned.
//
// One registry rather than a version literal in each layer, for two reasons.
// A pin scattered across five programs is five places to audit when a CVE
// lands, and it is five places for the same component to drift to different
// versions between environments. Here, an upgrade is a one-line diff a
// reviewer can see, and `task charts:outdated` has one file to compare
// against upstream.
//
// Floating tags are deliberately impossible: there is no "latest", and
// Version is a required field. A chart that resolves differently on Tuesday
// than it did on Monday is not reproducible infrastructure.
package charts

import (
	"fmt"
	"regexp"
	"slices"
)

// Chart is one pinned Helm chart.
type Chart struct {
	// Name is the chart name inside the repository, not the release name.
	Name string
	// Repo is the chart repository URL.
	Repo string
	// Version is the CHART version, which is not always the application
	// version — kube-prometheus-stack 90.0.0 ships Prometheus Operator
	// v0.93.1, and confusing the two produces a chart that does not exist.
	Version string
	// AppVersion is what the chart deploys, when anything needs to name it
	// independently — a validation image, say, which must be the version that
	// will actually run. Empty when the chart publishes none.
	AppVersion string
	// Namespace the release is installed into.
	Namespace string
}

// Registry of every chart this platform installs.
//
// Verified against the upstream repositories on 2026-09-09. Each entry names
// the application version it ships so a reader does not have to resolve the
// chart to know what is running.
var registry = map[string]Chart{
	// Layer 10 — CNI, and first for a reason the cloud-integration layer
	// explains. Cilium replaces kube-proxy in eBPF, which is why the
	// cluster tier disables kube-proxy in the Talos machine config.
	"cilium": {
		Name:       "cilium",
		Repo:       "https://helm.cilium.io",
		Version:    "1.20.1", // app 1.20.1
		AppVersion: "1.20.1",
		Namespace:  "kube-system",
	},

	// 10-node-platform, after the CNI. The CCM clears the `uninitialized` taint
	// Talos leaves on every node, so nothing schedules until it runs.
	"hcloud-ccm": {
		Name:      "hcloud-cloud-controller-manager",
		Repo:      "https://charts.hetzner.cloud",
		Version:   "1.36.0",
		Namespace: "kube-system",
	},
	"hcloud-csi": {
		Name:      "hcloud-csi",
		Repo:      "https://charts.hetzner.cloud",
		Version:   "2.23.0",
		Namespace: "kube-system",
	},

	// Layer 30 — core platform.
	"cert-manager": {
		Name:       "cert-manager",
		Repo:       "https://charts.jetstack.io",
		Version:    "v1.21.2", // app v1.21.2 — this chart tags with a leading v
		AppVersion: "v1.21.2",
		Namespace:  "cert-manager",
	},
	"external-secrets": {
		Name:       "external-secrets",
		Repo:       "https://charts.external-secrets.io",
		Version:    "2.10.0", // app v2.10.0
		AppVersion: "v2.10.0",
		Namespace:  "external-secrets",
	},
	"metrics-server": {
		Name:       "metrics-server",
		Repo:       "https://kubernetes-sigs.github.io/metrics-server/",
		Version:    "3.14.0", // app 0.9.0
		AppVersion: "0.9.0",
		Namespace:  "kube-system",
	},

	// Layer 40 — ingress.
	"ingress-nginx": {
		Name:       "ingress-nginx",
		Repo:       "https://kubernetes.github.io/ingress-nginx",
		Version:    "4.15.1", // app 1.15.1
		AppVersion: "1.15.1",
		Namespace:  "ingress-nginx",
	},

	// Layer 50 — GitOps.
	"argo-cd": {
		Name:       "argo-cd",
		Repo:       "https://argoproj.github.io/argo-helm",
		Version:    "10.9.0", // app v3.5.2
		AppVersion: "v3.5.2",
		Namespace:  "argocd",
	},

	// Layer 60 — observability. Metrics, logs and traces, with Alloy as the
	// collector: Promtail is deprecated upstream and Alloy is its replacement.
	"kube-prometheus-stack": {
		Name:       "kube-prometheus-stack",
		Repo:       "https://prometheus-community.github.io/helm-charts",
		Version:    "90.0.0", // app v0.93.1 (Prometheus Operator)
		AppVersion: "v0.93.1",
		Namespace:  "observability",
	},
	"loki": {
		Name:       "loki",
		Repo:       "https://grafana.github.io/helm-charts",
		Version:    "7.3.0", // app 3.6.12
		AppVersion: "3.6.12",
		Namespace:  "observability",
	},
	"tempo": {
		Name:       "tempo",
		Repo:       "https://grafana.github.io/helm-charts",
		Version:    "1.24.4", // app 2.9.0
		AppVersion: "2.9.0",
		Namespace:  "observability",
	},
	"alloy": {
		Name:       "alloy",
		Repo:       "https://grafana.github.io/helm-charts",
		Version:    "1.12.1", // app v1.19.2
		AppVersion: "v1.19.2",
		Namespace:  "observability",
	},
}

// versionPattern accepts the two spellings the registry uses — 1.2.3 and
// v1.2.3 — and rejects everything a floating tag would look like.
var versionPattern = regexp.MustCompile(`^v?\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)

// Get returns a chart by registry key.
func Get(key string) (Chart, error) {
	chart, known := registry[key]
	if !known {
		return Chart{}, fmt.Errorf("unknown chart %q; known charts: %v", key, Keys())
	}

	return chart, nil
}

// MustGet is Get for package-level initialisation where an unknown key is a
// programming error rather than a runtime condition.
func MustGet(key string) Chart {
	chart, err := Get(key)
	if err != nil {
		panic(err)
	}

	return chart
}

// Keys lists every registry key, sorted.
func Keys() []string {
	keys := make([]string, 0, len(registry))
	for key := range registry {
		keys = append(keys, key)
	}

	slices.Sort(keys)

	return keys
}

// All returns a copy of the registry, for tooling that reports on pins.
func All() map[string]Chart {
	out := make(map[string]Chart, len(registry))
	for key, chart := range registry {
		out[key] = chart
	}

	return out
}

// Validate checks every entry. It runs in a test rather than at init so a
// malformed pin fails the build rather than a deployment.
func (c Chart) Validate() error {
	if c.Name == "" {
		return fmt.Errorf("chart name is empty")
	}

	if c.Repo == "" {
		return fmt.Errorf("chart %q has no repository", c.Name)
	}

	if c.Namespace == "" {
		return fmt.Errorf("chart %q has no namespace", c.Name)
	}

	if !versionPattern.MatchString(c.Version) {
		return fmt.Errorf("chart %q version %q is not an exact version: floating tags are not reproducible", c.Name, c.Version)
	}

	return nil
}
