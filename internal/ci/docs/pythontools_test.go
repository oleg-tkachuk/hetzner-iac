package docs

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/ci/citest"
	"github.com/stretchr/testify/assert"
)

// otherPythonManager matches a Python package manager other than uv being
// installed or run: the CI steps, hooks, Brewfile entries and documentation
// that once fetched checkov and zizmor through pipx.
//
// uv is the one manager here, so that a pinned tool is fetched one way on a
// laptop, in a hook and in CI. A second manager is a second resolver, a second
// cache and a second answer to "which version ran".
var otherPythonManager = regexp.MustCompile(
	`\bpipx\b|\bpip3?\s+install\b|python3?\s+-m\s+pip\b|\b(?:poetry|pipenv|pdm|hatch)\s+(?:install|run|add|lock)\b|\b(?:conda|mamba)\s+install\b|\beasy_install\b`)

// TestTrackedFiles_RunPythonToolsThroughUv keeps that one manager.
//
// A file, not a line, is what the message names: the fix is to rewrite the
// step around uvx or `uv tool`, which a line number does not help with.
func TestTrackedFiles_RunPythonToolsThroughUv(t *testing.T) {
	t.Parallel()

	self := filepath.Join("internal", "ci", "docs", "pythontools_test.go")

	for _, name := range citest.Tracked(t, repoRoot) {
		if name == "" || name == self {
			continue
		}

		raw, err := os.ReadFile(filepath.Join(repoRoot, name))
		if err != nil {
			// A symlink or a submodule: nothing to read.
			continue
		}

		if found := otherPythonManager.FindAllString(string(raw), -1); len(found) > 0 {
			assert.Failf(t, "a Python manager other than uv",
				"%s uses %s. Run Python tools with uvx or `uv tool` instead",
				name, strings.Join(found, ", "))
		}
	}
}

func TestOtherPythonManager_Matches(t *testing.T) {
	t.Parallel()

	for text, want := range map[string]bool{
		`pipx install "checkov==3.4.0"`:   true,
		`pipx run zizmor`:                 true,
		`brew "pipx"`:                     true,
		`pip install -r requirements.txt`: true,
		`python3 -m pip install foo`:      true,
		`poetry install`:                  true,
		// Not an invocation: a hcloud Primary IP named pip, and the lockfile
		// names .gitattributes lists for linguist.
		`hcloud.NewPrimaryIp(ctx, "pip", args)`: false,
		`poetry.lock, pdm.lock, uv.lock`:        false,
		`uvx "checkov@3.4.0"`:                   false,
		`uv tool install zizmor`:                false,
	} {
		assert.Equal(t, want, otherPythonManager.MatchString(text), text)
	}
}
