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

// errorExit matches the line a tool prints before exiting non-zero, in either
// of the two spellings this repository has used.
var errorExit = regexp.MustCompile(`fmt\.(Fprintf|Fprintln)\(os\.Stderr, "error:`)

// wantedExit is the one it should be.
const wantedExit = `fmt.Fprintf(os.Stderr, "error: %v\n", err)`

// TestPrograms_ReportAFailureTheSameWay keeps every main() printing one line.
//
// The output was already identical — Fprintln with two arguments and Fprintf
// with %v produce the same bytes — so this is not about what an operator sees.
// It is about the next program: two spellings means the next author picks one
// at random, and then a third, and the taskfiles that grep for "error:" have
// nothing to rely on.
//
// Every main package, not just tools/. Reading only tools/*/main.go is how
// policy/main.go kept a `panic(err)` — a Go stack trace over the one sentence
// that matters, and exit 2 where everything else exits 1. A gate that misses
// a sibling directory is the third of that shape found in this repository.
func TestPrograms_ReportAFailureTheSameWay(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	mains, err := mainPackages(t, root)
	require.NoError(t, err)
	require.NotEmpty(t, mains, "no main package found; this test is checking nothing")

	var checked int

	for _, path := range mains {
		raw, readErr := os.ReadFile(path)
		require.NoError(t, readErr, path)

		body := string(raw)

		assert.NotContains(t, body, "panic(err)",
			"%s reports a failure by panicking: a stack trace instead of a sentence, and "+
				"exit 2 where every other program exits 1", relativeToRoot(path))

		for _, line := range strings.Split(body, "\n") {
			if !errorExit.MatchString(line) {
				continue
			}

			checked++

			assert.Equal(t, wantedExit, strings.TrimSpace(line),
				"%s prints a failure in a second spelling", relativeToRoot(path))
		}
	}

	assert.Positive(t, checked, "no program prints a failure; this test is checking nothing")
}

// mainPackages finds every main.go in a package clause of `main`, anywhere in
// the tree — tools, policy, the layers and the cluster tier alike.
func mainPackages(t *testing.T, root string) ([]string, error) {
	t.Helper()

	var found []string

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == "bin") {
			return filepath.SkipDir
		}

		// Any Go file in a main package, not main.go alone: a program that
		// splits its entry point from its wiring — layers/10-node-platform
		// does — would otherwise be able to move the failure line into a file
		// this never opens.
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
			strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}

		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}

		if strings.HasPrefix(string(raw), "package main") ||
			strings.Contains(string(raw), "\npackage main\n") {
			found = append(found, path)
		}

		return nil
	})

	return found, err
}
