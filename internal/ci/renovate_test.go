package ci

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

// ── What Renovate watches ───────────────────────────────────────────────────

// anchoredPattern strips Renovate's regex delimiters and the anchors, leaving
// the literal path a pattern watches. Both patterns in this repository are
// fully literal apart from one character class, which is why this can be a
// string operation rather than a regex engine.
var anchoredPattern = regexp.MustCompile(`^/\^(.+)\$/$`)

// TestRenovatePatterns_WatchPathsThatExist closes the hole that makes a
// renamed directory silent.
//
// A customManager whose managerFilePatterns match nothing is not an error to
// Renovate: it finds no dependency there, proposes no update, and says
// nothing — for ever. renovate.json's own comment names that failure for the
// regex inside the manager, and a test holds that half. Nothing held the
// path, so moving `pkg/charts/registry.go` would have stopped every chart
// update with no signal anywhere.
//
// Found while costing the move of pkg/ under internal/, which is exactly the
// change that would have triggered it.
func TestRenovatePatterns_WatchPathsThatExist(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	raw, err := os.ReadFile(filepath.Join(root, ".github", "renovate.json"))
	require.NoError(t, err)

	var config struct {
		CustomManagers []struct {
			ManagerFilePatterns []string `json:"managerFilePatterns"`
		} `json:"customManagers"`
	}

	require.NoError(t, json.Unmarshal(raw, &config))
	require.NotEmpty(t, config.CustomManagers, "no custom managers; this test is checking nothing")

	var checked int

	for _, manager := range config.CustomManagers {
		require.NotEmpty(t, manager.ManagerFilePatterns, "a custom manager watches no path at all")

		for _, pattern := range manager.ManagerFilePatterns {
			found := anchoredPattern.FindStringSubmatch(pattern)
			require.NotNil(t, found,
				"%q is not the anchored form this test can read; every pattern here is /^…$/", pattern)

			// The literal prefix, up to the first regex metacharacter. For a
			// pattern naming one file that is the whole path; for one naming a
			// directory of files it is the directory, which is what has to
			// exist.
			literal := found[1]
			if cut := strings.IndexAny(literal, `[](){}|+*?`); cut >= 0 {
				literal = literal[:cut]
			}

			literal = strings.ReplaceAll(literal, `\.`, ".")
			literal = strings.TrimSuffix(literal, "/")

			checked++

			_, err := os.Stat(filepath.Join(root, literal))
			assert.NoError(t, err,
				"a custom manager watches %q and %q does not exist, so Renovate reads no "+
					"dependency there and proposes no update, silently", pattern, literal)
		}
	}

	assert.Positive(t, checked, "no file patterns read; this test is checking nothing")
}

// ── When Renovate runs ──────────────────────────────────────────────────────

// hourConstrained matches a Renovate schedule that narrows the time of day —
// the `before`/`after` forms, and a cron whose hour field is not `*`.
var hourConstrained = regexp.MustCompile(`(?i)\b(before|after)\b|^\S+\s+[^*\s]`)

// TestRenovateScheduleIsNotNarrowerThanADay refuses a schedule that only a
// punctual cron could satisfy.
//
// Renovate runs here from a GitHub scheduled workflow, and GitHub runs those
// best-effort on shared runners. The schedule was `before 09:00 on monday`,
// which with `timezone: Europe/Kyiv` is Sunday 21:00 to Monday 06:00 UTC, while
// the workflow's cron fires at 06:00 UTC — exactly as that window shuts. The
// Monday pass started at 11:49 UTC, five hours and forty-nine
// minutes late, found three updates, and filed all three under "Awaiting
// Schedule". Renovate had opened no pull request in this repository, ever.
//
// Nothing reported it. `renovate-config-validator` accepts the broken schedule
// and the working one identically — checked, all four candidate forms pass — so
// validation cannot be the thing that catches this.
//
// A day-wide window still batches updates into one weekly review, which is the
// only thing the narrow one was for. What it drops is the dependency on a cron
// being punctual.
func TestRenovateScheduleIsNotNarrowerThanADay(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "renovate.json"))
	require.NoError(t, err)

	var config struct {
		Schedule []string `json:"schedule"`
	}

	require.NoError(t, json.Unmarshal(raw, &config))
	require.NotEmpty(t, config.Schedule, "no schedule in renovate.json")

	for _, entry := range config.Schedule {
		assert.False(t, hourConstrained.MatchString(strings.TrimSpace(entry)),
			"renovate.json schedule %q narrows the time of day. Renovate is triggered by a "+
				"GitHub cron, which is best-effort and was observed 5h49m late — a window "+
				"of hours is a window it misses. Keep the day and drop the hour.", entry)
	}
}

// TestRenovateCronIsDaily holds the other half of the pair.
//
// The daily cron is not redundant with a weekly schedule, and the reason
// changed when vulnerabilityAlerts were turned off. It was the security fast
// path: alerts were exempt from the schedule, so a fix could land on any
// morning. Now it is the best-effort cron — one Monday pass was observed 5h49m
// late, and a weekly cron that slips costs a week of updates while a daily one
// only has to be on time once.
//
// Either way the assertion is the same, which is why the test survived the
// change and only its name and reason did not.
func TestRenovateCronIsDaily(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "renovate.yaml"))
	require.NoError(t, err)

	cron := regexp.MustCompile(`(?m)^\s*-\s*cron:\s*"([^"]+)"`).FindStringSubmatch(string(raw))
	require.Len(t, cron, 2, "no cron in the renovate workflow")

	fields := strings.Fields(cron[1])
	require.Len(t, fields, 5, "cron %q is not five fields", cron[1])

	// Day-of-week and day-of-month both unconstrained: it runs every day.
	assert.Equal(t, "*", fields[4],
		"cron %q is not daily; vulnerabilityAlerts would then wait for it", cron[1])
	assert.Equal(t, "*", fields[2],
		"cron %q is not daily; vulnerabilityAlerts would then wait for it", cron[1])
}

// taskfileEntryPoints are the two files the taskfile pin manager watches.
var taskfileEntryPoints = []string{"Taskfile.yaml", "Taskfile.dev.yaml"}

// taskfilePinManager returns the manager that watches those two, found by what
// it watches rather than by position.
func taskfilePinManager(t *testing.T) []*regexp.Regexp {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "renovate.json"))
	require.NoError(t, err)

	var config renovateConfig
	require.NoError(t, json.Unmarshal(raw, &config))

	for _, manager := range config.CustomManagers {
		watches := false

		for _, pattern := range manager.ManagerFilePatterns {
			if strings.Contains(pattern, "Taskfile") {
				watches = true
			}
		}

		if !watches {
			continue
		}

		require.Len(t, manager.MatchStrings, 2,
			"one pattern for the pin inside a URL and one for the bare version")

		patterns := make([]*regexp.Regexp, 0, len(manager.MatchStrings))

		for _, matchString := range manager.MatchStrings {
			// Go accepts JavaScript's (?<name>…) syntax, so Renovate's own
			// pattern compiles here unchanged. One that stops being portable
			// fails this test rather than passing silently.
			pattern, compileErr := regexp.Compile(matchString)
			require.NoError(t, compileErr, "the configured pattern must be a valid regular expression")

			patterns = append(patterns, pattern)
		}

		return patterns
	}

	t.Fatal("no custom manager watches the task entry points — their pins are upgraded by nobody")

	return nil
}

// TestRenovateMatchesEveryTaskfilePin is the taskfile half of the guarantee
// TestRenovateMatchesEveryPinnedTool gives the workflows.
//
// Both directions, because each failure is silent in its own way. A pin the
// pattern does not match is a dependency nobody upgrades, and the repository
// looks maintained because every other bot pull request keeps arriving. An
// annotation with no pin under it is a pattern that has drifted from the file
// it was written for.
func TestRenovateMatchesEveryTaskfilePin(t *testing.T) {
	t.Parallel()

	patterns := taskfilePinManager(t)

	// The two pins, and which file each is expected in: TASKLIB is written
	// twice because Task resolves `includes:` before it loads `dotenv:`, so
	// there is no third file both entry points could read it from.
	wanted := map[string]int{
		"oleg-tkachuk/taskfiles":             2,
		"github.com/oleg-tkachuk/pulumi-kit": 1,
	}

	found := map[string]int{}

	annotation := regexp.MustCompile(`# renovate: datasource=(\S+) depName=(\S+)`)

	// The datasources these two pins resolve from: GitHub tags for the task
	// library, and the Go module proxy for pulumi-kit.
	allowed := map[string]bool{"github-tags": true, "go": true}

	for _, name := range taskfileEntryPoints {
		raw, err := os.ReadFile(filepath.Join("..", "..", name))
		require.NoError(t, err, name)

		text := string(raw)

		annotations := annotation.FindAllStringSubmatch(text, -1)
		require.NotEmpty(t, annotations, "%s carries no renovate annotation", name)

		var matched int

		for _, pattern := range patterns {
			for _, match := range pattern.FindAllStringSubmatch(text, -1) {
				matched++

				index := pattern.SubexpIndex("depName")
				require.GreaterOrEqual(t, index, 0, "the pattern has no depName group")

				found[match[index]]++
			}
		}

		assert.Equal(t, len(annotations), matched,
			"%s carries %d annotation(s) and the configured patterns match %d pin(s): "+
				"an annotated pin the pattern misses is a dependency nobody upgrades",
			name, len(annotations), matched)

		for _, match := range annotations {
			assert.True(t, allowed[match[1]],
				"%s: datasource %q is not one these pins resolve from", name, match[1])

			if match[1] == "github-tags" {
				assert.Contains(t, match[2], "/",
					"%s: github-tags depName %q is not owner/repo", name, match[2])
			}
		}
	}

	assert.Equal(t, wanted, found,
		"the pins Renovate can see are %v, and the ones that must be watched are %v", found, wanted)
}
