// Package charts is the single place every Helm chart is described.
//
// One file per chart, and that file is the whole of what this repository knows
// about it: the pin, the release name, the layer that installs it and the
// objects it is expected to produce. The packages that used to hold those
// halves separately read them from here.
//
// One registry rather than a version literal in each layer, for two reasons.
// A pin scattered across five programs is five places to audit when a CVE
// lands, and it is five places for the same component to drift to different
// versions between environments. Here, an upgrade is a one-line diff a
// reviewer can see, and `charts:outdated` has one file to compare
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
//
// Name, Repo and Version are written as STRING LITERALS in every declaration,
// in that order, and never as constants. Renovate reads them out of these
// files with a regular expression — see .github/renovate.json — so a constant
// there is a chart Renovate stops matching, and its answer to that is not an
// error but silence: no pull request, for ever. Measured the moment it
// happened: replacing one Name with a constant failed
// TestRenovatePatternMatchesEveryChart for argo-cd and nothing else noticed.
type Chart struct {
	// Name is the chart name inside the repository, not the release name.
	Name string
	// Repo is the chart repository URL.
	Repo string
	// Version is the CHART version, which is not always the application
	// version — cilium's chart 1.20.1 happens to match, argo-cd's 10.9.0
	// ships app v3.5.2, and confusing the two produces a chart that does not
	// exist.
	Version string
	// AppVersion is what the chart deploys, when anything needs to name it
	// independently — a validation image, say, which must be the version that
	// will actually run. Empty when the chart publishes none.
	AppVersion string
	// Namespace the release is installed into.
	Namespace string
}

// versionPattern accepts the two spellings the registry uses — 1.2.3 and
// v1.2.3 — and rejects everything a floating tag would look like.
var versionPattern = regexp.MustCompile(`^v?\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)

// Get returns a chart's pin by registry key.
//
// The pin rather than the whole definition, because that is what a layer
// installing it needs — Lookup returns the rest.
func Get(key string) (Chart, error) {
	definition, err := Lookup(key)
	if err != nil {
		return Chart{}, err
	}

	return definition.Chart, nil
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
	keys := make([]string, 0, len(definitions))
	for key := range definitions {
		keys = append(keys, key)
	}

	slices.Sort(keys)

	return keys
}

// All returns a copy of the pins, for tooling that reports on them.
func All() map[string]Chart {
	out := make(map[string]Chart, len(definitions))
	for key, definition := range definitions {
		out[key] = definition.Chart
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
