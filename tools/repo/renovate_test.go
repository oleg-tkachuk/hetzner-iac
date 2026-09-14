package repo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// renovateConfig is the part of .github/renovate.json this file reads.
type renovateConfig struct {
	CustomManagers []struct {
		Description         string   `json:"description"`
		ManagerFilePatterns []string `json:"managerFilePatterns"`
		MatchStrings        []string `json:"matchStrings"`
	} `json:"customManagers"`
}

// toolVersionsManager returns the manager that watches the workflows, found by
// what it watches rather than by position: a manager added in front of it
// would otherwise silently move this test onto the wrong one.
func toolVersionsManager(t *testing.T) *regexp.Regexp {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "renovate.json"))
	require.NoError(t, err)

	var config renovateConfig
	require.NoError(t, json.Unmarshal(raw, &config))

	for _, manager := range config.CustomManagers {
		watchesWorkflows := false

		for _, pattern := range manager.ManagerFilePatterns {
			if strings.Contains(pattern, ".github/workflows") {
				watchesWorkflows = true
			}
		}

		if !watchesWorkflows {
			continue
		}

		require.Len(t, manager.MatchStrings, 1, "one pattern for the workflow pins")

		// Go accepts JavaScript's (?<name>…) syntax, so Renovate's own pattern
		// compiles here unchanged. One that stops being portable fails this
		// test rather than passing silently.
		pattern, err := regexp.Compile(manager.MatchStrings[0])
		require.NoError(t, err, "the configured pattern must be a valid regular expression")

		return pattern
	}

	t.Fatal("no custom manager watches .github/workflows — the pinned tools are upgraded by nobody")

	return nil
}

// workflowPin is one `NAME_VERSION: "value"` line in a workflow.
type workflowPin struct {
	file  string
	name  string
	value string
}

// pinnedToolVersions finds every pin in the workflows, annotated or not. This
// is deliberately a different expression from Renovate's: comparing the two is
// the point, and a shared helper would agree with itself.
func pinnedToolVersions(t *testing.T) []workflowPin {
	t.Helper()

	dir := filepath.Join("..", "..", ".github", "workflows")

	entries, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, entries, "no workflows found")

	line := regexp.MustCompile(`(?m)^\s*([A-Z0-9_]+_VERSION):\s*"([^"]+)"`)

	var pins []workflowPin

	for _, path := range entries {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)

		for _, match := range line.FindAllStringSubmatch(string(raw), -1) {
			pins = append(pins, workflowPin{
				file:  filepath.Base(path),
				name:  match[1],
				value: match[2],
			})
		}
	}

	return pins
}

// TestRenovateMatchesEveryPinnedTool runs Renovate's own regex, read out of its
// own configuration, against the workflows.
//
// The failure it guards is silent and slow, and it is the reason the chart
// registry has the same test: change the shape of a pin, or add one without an
// annotation, and Renovate does not error — it opens no pull request for that
// tool, for ever, and the only symptom is a version that never moves. Every
// pin here was verified current when it was added, so nothing being stale today is
// exactly what makes the silence hard to notice tomorrow.
func TestRenovateMatchesEveryPinnedTool(t *testing.T) {
	t.Parallel()

	pattern := toolVersionsManager(t)
	pins := pinnedToolVersions(t)

	matched := map[string]string{}

	for _, path := range []string{"ci.yaml", "security.yaml"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", path))
		require.NoError(t, err)

		for _, match := range pattern.FindAllStringSubmatch(string(raw), -1) {
			byName := map[string]string{}

			for i, name := range pattern.SubexpNames() {
				if name != "" {
					byName[name] = match[i]
				}
			}

			require.NotEmpty(t, byName["datasource"], "a match with no datasource in %s", path)
			require.NotEmpty(t, byName["depName"], "a match with no depName in %s", path)

			matched[byName["currentValue"]] = byName["depName"]
		}
	}

	// Every pin the workflows declare has to be one Renovate found. The
	// assertion is per-pin rather than a count, so the message names the tool
	// nobody would upgrade.
	for _, pin := range pins {
		_, found := matched[pin.value]
		assert.True(t, found,
			"%s in %s is pinned to %q and Renovate's pattern does not match it — "+
				"add a `# renovate: datasource=… depName=…` comment above it, or it is upgraded by nobody",
			pin.name, pin.file, pin.value)
	}

	assert.Len(t, matched, len(pins),
		"the pattern matched something that is not one of the %d pins", len(pins))
}

// TestRenovateAnnotationsNameARealDatasource keeps the annotations to the three
// datasources these tools actually come from.
//
// A typo in the datasource is the same silent failure as no annotation at all:
// Renovate skips a dependency whose datasource it does not know, and says so
// only in its own logs.
func TestRenovateAnnotationsNameARealDatasource(t *testing.T) {
	t.Parallel()

	annotation := regexp.MustCompile(`# renovate: datasource=(\S+) depName=(\S+)`)

	// The three this repository installs from: GitHub release tarballs, pipx
	// from PyPI, and `go install`.
	allowed := map[string]bool{"github-releases": true, "pypi": true, "go": true}

	var seen int

	for _, path := range []string{"ci.yaml", "security.yaml"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", path))
		require.NoError(t, err)

		for _, match := range annotation.FindAllStringSubmatch(string(raw), -1) {
			seen++

			assert.True(t, allowed[match[1]],
				"%s: datasource %q is not one this repository installs from", path, match[1])

			// A GitHub datasource needs owner/repo; the others are bare names.
			// Getting this wrong resolves to nothing, quietly.
			if match[1] == "github-releases" {
				assert.Contains(t, match[2], "/",
					"%s: github-releases depName %q is not owner/repo", path, match[2])
			}
		}
	}

	assert.Positive(t, seen, "no annotations found — this test is checking nothing")
}

// TestRenovateStripsTheVWhereThePinOmitsIt guards the mismatch that makes a
// bot look broken while it is working exactly as configured.
//
// Renovate compares the pin against upstream's tags. kubeconform, gitleaks and
// trivy tag `v0.8.0` while the pins here read `0.8.0`, because the workflows
// add the `v` themselves in the download URL. Without extractVersion on those
// three, Renovate compares "0.8.0" against a list of "v…" and finds nothing to
// do.
func TestRenovateStripsTheVWhereThePinOmitsIt(t *testing.T) {
	t.Parallel()

	pattern := toolVersionsManager(t)

	for _, path := range []string{"ci.yaml", "security.yaml"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", path))
		require.NoError(t, err)

		for _, match := range pattern.FindAllStringSubmatch(string(raw), -1) {
			byName := map[string]string{}

			for i, name := range pattern.SubexpNames() {
				if name != "" {
					byName[name] = match[i]
				}
			}

			// Only the GitHub datasource compares against tags, which is where
			// the `v` lives. PyPI and Go versions need no stripping.
			if byName["datasource"] != "github-releases" {
				continue
			}

			if strings.HasPrefix(byName["currentValue"], "v") {
				assert.Empty(t, byName["extractVersion"],
					"%s: %s already carries its v, so extractVersion would strip it twice",
					path, byName["depName"])

				continue
			}

			assert.NotEmpty(t, byName["extractVersion"],
				"%s: %s is pinned as %q without a v, and upstream tags carry one — "+
					"it needs extractVersion or Renovate will never match it",
				path, byName["depName"], byName["currentValue"])
		}
	}
}

// TestRenovatePinsDigestsForActionsOnly holds the scope of the one rule that
// turns a supply-chain control into a repository problem when it is too wide.
//
// Pinning a `uses:` to a commit is the control: a tag is mutable, and a moved
// tag is a supply-chain change nothing would notice. But the github-actions
// manager reports a step's INPUTS as dependencies too, with depType
// `uses-with` — and a version input has no digest.
//
// Measured from Renovate's own debug log. With the rule on the
// whole manager it tried to write helm/helm's commit into azure/setup-helm's
// `version: v4.3.0`, then could not read it back:
//
//	Digest is not updated
//	  depName: "helm", manager: "github-actions"
//	  expectedValue: "bec5b06ed841fe5269972d864d5177944fd5970f"
//	  foundValue: undefined
//	WARN: Error updating branch: update failure
//
// The dashboard carried that as a repository problem on every run, and the
// update was retried for ever because it cannot succeed.
func TestRenovatePinsDigestsForActionsOnly(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "renovate.json"))
	require.NoError(t, err)

	var config struct {
		PackageRules []struct {
			MatchManagers []string `json:"matchManagers"`
			MatchDepTypes []string `json:"matchDepTypes"`
			PinDigests    *bool    `json:"pinDigests"`
		} `json:"packageRules"`
	}

	require.NoError(t, json.Unmarshal(raw, &config))

	var checked int

	for _, rule := range config.PackageRules {
		if rule.PinDigests == nil || !*rule.PinDigests {
			continue
		}

		checked++

		assert.Equal(t, []string{"action"}, rule.MatchDepTypes,
			"a pinDigests rule is not scoped to depType `action`; the same manager reports "+
				"step inputs as `uses-with` dependencies, and a version input has no digest to pin")
	}

	assert.Equal(t, 1, checked,
		"exactly one rule pins digests; the count changed and the scope above may not cover it")
}
