package ci

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// execCall matches a binary this repository runs from Go, by its literal name.
// A name held in a variable is not matched and does not need to be: the two
// that exist — talosctl and golangci-lint — are resolved by tools that exist
// to pin them, and both are documented.
var execCall = regexp.MustCompile(`exec\.Command(?:Context)?\((?:ctx,\s*)?"([a-z][a-z0-9-]*)"`)

// declaredDependency matches the precondition a task uses to say it needs
// something installed. It is the repository's own convention — every task says
// what to install rather than skipping itself silently — and therefore the
// list of what an operator has to have.
var declaredDependency = regexp.MustCompile(`command -v ([a-z][a-z0-9-]*)`)

// providedByTheOS are the ones no list should carry, and the Brewfile says why
// for two of them: "curl and git ship with macOS, so neither is listed". The
// rest are POSIX utilities in the same category.
var providedByTheOS = map[string]bool{
	"awk": true, "curl": true, "git": true, "tar": true, "install": true,
	"uname": true, "mktemp": true, "sed": true, "grep": true, "printf": true,
}

// wherePeopleLook for a tool before running anything: the prerequisites in the
// README, the checks' own list in ci.md, and the Brewfile that installs them.
//
// Any of the three, not all: kubectl belongs in the prerequisites and lychee
// belongs with the checks, and neither should be repeated into the other.
var wherePeopleLook = []string{"README.md", filepath.Join("docs", "ci.md"), "Brewfile"}

// TestExternalTools_AreNamedWhereSomebodyWouldLook keeps the lists of what to
// install from drifting away from what the code runs.
//
// Two were missing when this was written, and both had been for a while.
// `kubectl` is run by tools/orphans and by four cluster tasks, and appeared in
// no list at all. `hubble` is a precondition of task cluster:hubble and was in
// none either. Each task does say what to install when it is missing — that
// convention held — but somebody reading the prerequisites before starting had
// no way to know.
func TestExternalTools_AreNamedWhereSomebodyWouldLook(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	needed := map[string][]string{}

	for _, path := range tracked(t, root) {
		switch {
		case strings.HasSuffix(path, "_test.go"):
			continue
		case strings.HasSuffix(path, ".go"):
			record(t, root, path, execCall, needed)
		case path == "Taskfile.yaml" || strings.HasPrefix(path, "tasks/"):
			record(t, root, path, declaredDependency, needed)
		}
	}

	require.NotEmpty(t, needed, "no external tool was found, so this test proved nothing")

	var documented string

	for _, name := range wherePeopleLook {
		raw, err := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, err, name)

		documented += string(raw)
	}

	var checked int

	for tool, callers := range needed {
		if providedByTheOS[tool] {
			continue
		}

		checked++

		assert.Contains(t, documented, tool,
			"%s is run by %s and is named in none of %s, so somebody reading what to install "+
				"before starting cannot know they need it",
			tool, strings.Join(callers, ", "), strings.Join(wherePeopleLook, ", "))
	}

	assert.Positive(t, checked, "every tool found was one the OS provides, which cannot be right")
}

// record adds every match in one file to the set, remembering which file asked
// for it so a failure names the caller rather than only the tool.
func record(t *testing.T, root, path string, pattern *regexp.Regexp, into map[string][]string) {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(root, path))
	require.NoError(t, err, path)

	for _, found := range pattern.FindAllStringSubmatch(string(raw), -1) {
		into[found[1]] = append(into[found[1]], path)
	}
}
