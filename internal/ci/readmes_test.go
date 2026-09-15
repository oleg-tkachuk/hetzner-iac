package ci

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readmeMinimum is the length below which a README is a placeholder rather
// than a document. The shortest real one here is several times this.
const readmeMinimum = 200

// documentedTrees are the directories whose every child documents itself, and
// which document themselves.
var documentedTrees = []string{"tools", "internal"}

// TestEveryToolDocumentsItself asks each tool and each internal package for a
// README.
//
// A directory named for its subject says what it is about and nothing about
// what it answers, which flag it takes, or who runs it. That was the state
// before these: eleven tools whose only description was a doc comment in
// main.go, so reading one meant opening Go source to find out whether it even
// takes an argument.
//
// The first line must be the directory's own name. A README that opens with
// another tool's heading is a copy left unfinished, and it reads as correct.
func TestEveryToolDocumentsItself(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	var checked int

	for _, tree := range documentedTrees {
		assertREADME(t, root, tree, tree)

		entries, err := os.ReadDir(filepath.Join(root, tree))
		require.NoError(t, err, tree)

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}

			assertREADME(t, root, filepath.Join(tree, entry.Name()), entry.Name())

			checked++
		}
	}

	assert.Positive(t, checked, "no directories found under any documented tree; this test is checking nothing")
}

// assertREADME checks that dir holds a README that is a document and opens
// with the heading name.
func assertREADME(t *testing.T, root, dir, name string) {
	t.Helper()

	path := filepath.Join(dir, "README.md")

	raw, err := os.ReadFile(filepath.Join(root, path))
	if !assert.NoError(t, err, "%s has no README; a directory named for its subject says nothing about what it answers", dir) {
		return
	}

	assert.GreaterOrEqual(t, len(raw), readmeMinimum,
		"%s is %d bytes, which is a placeholder rather than a document", path, len(raw))

	heading := strings.SplitN(string(raw), "\n", 2)[0]
	assert.Equal(t, "# "+name, heading,
		"%s opens with %q instead of its own name, which is what an unfinished copy looks like",
		path, heading)
}
