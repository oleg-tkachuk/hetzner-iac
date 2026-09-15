package ci

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGoBuild_AlwaysNamesAnOutputPath covers the cause; TestGitignore_CoversEveryBinaryName
// covers the consequence.
//
// `go build ./...` with no -o names each binary after its main package and
// leaves it in the working directory, where those names are the tools/, layers/
// and infra/ directory names. `task build` and ci.yaml both pass `-o bin/`, and
// nothing held them to it: this repository has had a 52 MB layer binary
// committed once and two more reach the tree since, and the working root
// carried 470 MB of them when this test was written.
//
// Only files that run commands are read. A documented command is one step
// removed — docs/operations.md's `go build -o smoke ./tools/smoke` is correct
// today — and prose about the problem would fail a scan of markdown for no
// gain.
func TestGoBuild_AlwaysNamesAnOutputPath(t *testing.T) {
	t.Parallel()

	var checked int

	for _, path := range commandFiles(t) {
		raw, err := os.ReadFile(path)
		require.NoError(t, err, path)

		for number, line := range strings.Split(string(raw), "\n") {
			// A comment, and two of them say `go build ./...` on purpose:
			// Taskfile.yaml explains why the -o is there and ci.yaml explains
			// why there is no separate build step at all.
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				continue
			}

			build := strings.Index(trimmed, "go build")
			if build < 0 {
				continue
			}

			checked++

			assert.Contains(t, trimmed[build:], "-o ",
				"%s:%d runs `go build` with no -o, so it writes a binary named after each "+
					"main package into whatever directory it runs in", path, number+1)
		}
	}

	assert.Positive(t, checked, "no `go build` found in any taskfile, workflow or hook; this test is checking nothing")
}

// commandFiles are the files in this repository that run shell commands: the
// taskfiles, the CI workflows and the git hooks.
func commandFiles(t *testing.T) []string {
	t.Helper()

	root := filepath.Join("..", "..")

	paths := []string{
		filepath.Join(root, "Taskfile.yaml"),
		filepath.Join(root, "lefthook.yml"),
	}

	for _, dir := range []string{
		filepath.Join(root, "tasks"),
		filepath.Join(root, ".github", "workflows"),
	} {
		entries, err := os.ReadDir(dir)
		require.NoError(t, err, dir)

		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || (!strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml")) {
				continue
			}

			paths = append(paths, filepath.Join(dir, name))
		}
	}

	for _, path := range paths {
		_, err := os.Stat(path)
		require.NoError(t, err, "%s is in the list and does not exist", path)
	}

	return paths
}
