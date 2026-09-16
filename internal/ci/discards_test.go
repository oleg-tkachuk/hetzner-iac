package ci

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// discard matches an error thrown away with the blank identifier.
var discard = regexp.MustCompile(`(^|\s)_ = |{ _ = `)

// commentLine matches a Go line comment, which is what an explanation looks
// like.
var commentLine = regexp.MustCompile(`^\s*//`)

// discardExplanation is how many lines above a discard are searched for one.
// Three, which is a short paragraph — the reasons worth writing here do not
// fit on one line and do not need ten.
const discardExplanation = 3

// TestDiscardedErrors_SayWhy is nolintlint's rule applied to the thing it does
// not cover.
//
// `//nolint` needs an explanation in this repository, and a bare `_ =` needs
// nothing — so the discards nobody argued for looked exactly like the ones
// that were thought about. Every one here turned out to be correct, and not
// one of them said so; the two in pulumilog are the interesting case, because
// the only way to report that logging failed is to log.
//
// Not a ban. A discarded error is often right — a Close on a read-only handle,
// a temp file that failed to delete. What is never right is leaving the next
// reader to work out which kind it is.
func TestDiscardedErrors_SayWhy(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	var checked int

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if entry.IsDir() {
			// Vendored, generated and build output: not ours to annotate.
			if name := entry.Name(); name == ".git" || name == "bin" || name == ".cache" {
				return filepath.SkipDir
			}

			return nil
		}

		// Tests discard freely and legibly — a t.TempDir cleanup needs no
		// paragraph — and holding them to this would turn the gate into noise.
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}

		lines := strings.Split(string(raw), "\n")

		for i, line := range lines {
			if !discard.MatchString(line) {
				continue
			}

			checked++

			explained := false

			for back := 1; back <= discardExplanation && i-back >= 0; back++ {
				if commentLine.MatchString(lines[i-back]) {
					explained = true

					break
				}
			}

			assert.True(t, explained,
				"%s:%d throws an error away with no comment saying why:\n\t%s\n"+
					"A discarded error is often right — say which kind it is",
				relativeToRoot(path), i+1, strings.TrimSpace(line))
		}

		return nil
	})
	require.NoError(t, err)

	assert.Positive(t, checked, "no discarded error was examined; the pattern is broken")
}
