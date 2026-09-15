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

// errorExit matches the line a tool prints before exiting non-zero, in either
// of the two spellings this repository has used.
var errorExit = regexp.MustCompile(`fmt\.(Fprintf|Fprintln)\(os\.Stderr, "error:`)

// wantedExit is the one it should be.
const wantedExit = `fmt.Fprintf(os.Stderr, "error: %v\n", err)`

// TestTools_ReportAFailureTheSameWay keeps eleven programs printing one line.
//
// The output was already identical — Fprintln with two arguments and Fprintf
// with %v produce the same bytes — so this is not about what an operator
// sees. It is about the next tool: two spellings in one directory means the
// next author picks one at random, and then a third, and the taskfiles that
// grep for "error:" have nothing to rely on.
func TestTools_ReportAFailureTheSameWay(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..", "tools")

	entries, err := os.ReadDir(root)
	require.NoError(t, err)

	var checked int

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		path := filepath.Join(root, entry.Name(), "main.go")

		raw, readErr := os.ReadFile(path)
		if os.IsNotExist(readErr) {
			continue
		}

		require.NoError(t, readErr, path)

		for _, line := range strings.Split(string(raw), "\n") {
			if !errorExit.MatchString(line) {
				continue
			}

			checked++

			assert.Equal(t, wantedExit, strings.TrimSpace(line),
				"%s prints a failure in a second spelling", relativeToRoot(path))
		}
	}

	assert.Positive(t, checked, "no tool prints a failure; this test is checking nothing")
}
