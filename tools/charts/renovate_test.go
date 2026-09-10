package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/charts"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// renovateConfig is the part of .github/renovate.json this test cares about.
type renovateConfig struct {
	CustomManagers []struct {
		ManagerFilePatterns []string `json:"managerFilePatterns"`
		MatchStrings        []string `json:"matchStrings"`
		Datasource          string   `json:"datasourceTemplate"`
	} `json:"customManagers"`
}

// TestRenovatePatternMatchesEveryChart runs Renovate's own regex, read out of
// its own configuration, against the registry.
//
// The failure it guards is silent and slow: reorder the fields in an entry, or
// add a chart in a different shape, and Renovate stops matching. It does not
// error — it simply opens no pull request for that chart, for ever, and the
// only symptom is an upgrade that never arrives.
//
// Go accepts JavaScript's (?<name>…) group syntax since 1.22, so the same
// pattern compiles here. A pattern that stops being portable will fail this
// test rather than pass silently.
func TestRenovatePatternMatchesEveryChart(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	raw, err := os.ReadFile(filepath.Join(root, ".github", "renovate.json"))
	require.NoError(t, err)

	var config renovateConfig
	require.NoError(t, json.Unmarshal(raw, &config))
	require.Len(t, config.CustomManagers, 1, "one custom manager, for the chart registry")

	manager := config.CustomManagers[0]
	assert.Equal(t, "helm", manager.Datasource)
	require.Len(t, manager.MatchStrings, 1)

	pattern, err := regexp.Compile(manager.MatchStrings[0])
	require.NoError(t, err, "the configured pattern must be a valid regular expression")

	registry, err := os.ReadFile(filepath.Join(root, "pkg", "charts", "registry.go"))
	require.NoError(t, err)

	found := map[string]struct{ repo, version string }{}

	for _, match := range pattern.FindAllStringSubmatch(string(registry), -1) {
		byName := map[string]string{}

		for i, name := range pattern.SubexpNames() {
			if name != "" {
				byName[name] = match[i]
			}
		}

		found[byName["depName"]] = struct{ repo, version string }{
			repo:    byName["registryUrl"],
			version: byName["currentValue"],
		}
	}

	// Keyed by chart Name, not by registry key: those differ for hcloud-ccm,
	// whose key is short and whose chart is called
	// hcloud-cloud-controller-manager. Renovate sees the chart name.
	for _, key := range charts.Keys() {
		chart := charts.MustGet(key)

		match, matched := found[chart.Name]
		require.True(t, matched,
			"Renovate's pattern does not match chart %q (key %q) — it would never be upgraded", chart.Name, key)

		assert.Equal(t, chart.Repo, match.repo, chart.Name)
		assert.Equal(t, chart.Version, match.version, chart.Name)
	}

	assert.Len(t, found, len(charts.Keys()), "the pattern matched something that is not a chart")
}

// TestRenovateWatchesTheRegistryFile pins the path, because a moved registry
// with a stale pattern is the same silent failure.
func TestRenovateWatchesTheRegistryFile(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "renovate.json"))
	require.NoError(t, err)

	var config renovateConfig
	require.NoError(t, json.Unmarshal(raw, &config))
	require.Len(t, config.CustomManagers, 1)

	assert.Equal(t,
		[]string{"/^pkg/charts/registry\\.go$/"},
		config.CustomManagers[0].ManagerFilePatterns)
}
