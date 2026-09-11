// Package repo holds the checks that belong to the repository rather than to
// any one tool.
//
// Test files only: there is no command here. This one was written inside
// tools/topology, which parses cluster topologies and has nothing to do with
// .gitignore — a test in the wrong package is a test nobody looks for.
package repo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGitignore_CoversEveryBinaryName guards a list that went stale three
// times.
//
// `go build -o bin/ ./...` is what `task build` runs, and bin/ is ignored as
// one line. But a bare `go build ./...` still drops a binary named after each
// main package into the working directory, and those names are the tools/,
// layers/ and infra/ directory names. Two tools were added without their
// names, and `infra/cluster` — whose binary is called `cluster` — was never
// there at all; that one was found by building into a scratch directory and
// reading what came out, not by review.
//
// The pre-commit hook is the real guarantee, because it asks git what is
// binary rather than matching a name. This keeps `git status` honest.
func TestGitignore_CoversEveryBinaryName(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	raw, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	require.NoError(t, err)

	ignored := map[string]bool{}

	for _, line := range strings.Split(string(raw), "\n") {
		ignored[strings.TrimSpace(line)] = true
	}

	for _, parent := range []string{"tools", "layers", "infra"} {
		entries, readErr := os.ReadDir(filepath.Join(root, parent))
		require.NoError(t, readErr, parent)

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}

			name := entry.Name()

			// Only a main package produces a binary. tools/awk is an awk
			// script with Go tests around it, and tools/repo is test files
			// only — neither leaves anything for git to see, and requiring an
			// ignore for them was this test's own first bug.
			if !hasMainPackage(t, filepath.Join(root, parent, name)) {
				continue
			}

			// Layers are numbered, and one pattern covers all of them.
			if parent == "layers" {
				assert.True(t, ignored["/[0-9][0-9]-*"],
					".gitignore must keep the /[0-9][0-9]-* pattern for layer binaries")

				continue
			}

			assert.True(t, ignored["/"+name],
				".gitignore has no /%s, so a bare `go build ./...` leaves %s/%s's binary "+
					"untracked in the repository root", name, parent, name)
		}
	}
}

// hasMainPackage reports whether a directory holds a Go main package, which is
// the only thing `go build` turns into a binary.
func hasMainPackage(t *testing.T, dir string) bool {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err, dir)

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		raw, readErr := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, readErr)

		for _, line := range strings.Split(string(raw), "\n") {
			if strings.TrimSpace(line) == "package main" {
				return true
			}
		}
	}

	return false
}
