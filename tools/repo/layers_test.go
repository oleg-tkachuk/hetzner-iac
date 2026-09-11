package repo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEveryLayer_ChecksItsComponents closes the gap that made the other checks
// optional.
//
// pkg/layer/layertest holds the invariants a component set must satisfy — the
// chart is pinned, the order has no cycle, every chart has workloads declared.
// Nothing made a layer call it. A new layer added without that one line got
// none of them and nothing said so, which is the same shape as every other
// drift this repository has fixed today: two halves, and no test comparing
// them.
func TestEveryLayer_ChecksItsComponents(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	entries, err := os.ReadDir(filepath.Join(root, "layers"))
	require.NoError(t, err)

	var checked int

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		layer := entry.Name()
		dir := filepath.Join(root, "layers", layer)

		// A layer that declares no component set has nothing to check: it
		// builds its resources by hand, which is allowed and is what
		// 05-object-storage did before it was removed.
		if !contains(t, dir, "layer.Components{") {
			continue
		}

		assert.True(t, contains(t, dir, "layertest.Check(t, Components)"),
			"layers/%s declares a component set but never calls layertest.Check, "+
				"so none of the invariants are asserted for it", layer)

		checked++
	}

	assert.Positive(t, checked, "no layer declares a component set — this test is checking nothing")
}

// contains reports whether any Go file in dir holds the given text.
func contains(t *testing.T, dir, text string) bool {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err, dir)

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}

		raw, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
		require.NoError(t, readErr)

		if strings.Contains(string(raw), text) {
			return true
		}
	}

	return false
}
