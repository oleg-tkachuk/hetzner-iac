package docs

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// layerReference matches a layer named by its path, which is the only spelling
// this test can check — and therefore the spelling every comment must use.
//
// A bare "10-cni" could be a directory that once existed or a sentence about
// one, and no test can tell those apart. With the path prefix required, a
// reference either resolves or it is a defect, and prose about a layer that
// was removed is exempt by construction.
var layerReference = regexp.MustCompile(`layers/[0-9]{2}-[a-z][a-z0-9-]*`)

// TestEveryLayerReference_PointsAtADirectoryThatExists catches the comment
// that outlives the thing it describes.
//
// Merging two layers left eight comments pointing readers at two directories
// that no longer exist — 10-cni and 20-cloud-integration — including the
// package that explains why the CNI is not installed with the cluster, which
// is exactly where a reader goes to understand that. One was found by eye,
// months later. Nothing would have found the rest.
//
// The names above are deliberately written without the path prefix: this test
// reads its own source like any other file.
func TestEveryLayerReference_PointsAtADirectoryThatExists(t *testing.T) {
	t.Parallel()

	root := repoRoot

	entries, err := os.ReadDir(filepath.Join(root, "layers"))
	require.NoError(t, err)

	existing := map[string]bool{}

	for _, entry := range entries {
		if entry.IsDir() {
			existing["layers/"+entry.Name()] = true
		}
	}

	require.NotEmpty(t, existing, "no layers found — this test is checking nothing")

	var references int

	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if entry.IsDir() {
			// .git holds every version of every file, including the ones that
			// did name the layers that are gone.
			if entry.Name() == ".git" {
				return fs.SkipDir
			}

			return nil
		}

		if !slices.Contains([]string{".go", ".yaml", ".yml", ".json", ".md"}, filepath.Ext(entry.Name())) {
			return nil
		}

		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}

		for _, reference := range layerReference.FindAllString(string(raw), -1) {
			references++

			assert.True(t, existing[reference],
				"%s names %s, which is not a layer in this tree", path, reference)
		}

		return nil
	})
	require.NoError(t, err)

	assert.Positive(t, references, "no file names a layer by path — the pattern must be wrong")
}
