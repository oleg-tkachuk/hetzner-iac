package main

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
