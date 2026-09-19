package ci

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/charts"
)

// chartsPackage is where a chart is declared, one file per chart.
const chartsPackage = "internal/pkg/charts"

// notAChart are the package's own files: the types and the registry API, which
// declare no chart.
var notAChart = map[string]bool{
	"definition.go": true,
	"registry.go":   true,
}

// TestChartFiles_AreOnePerChart holds the directory equal to the registry.
//
// The point of one file per chart is that the file is the whole of what this
// repository knows about it — the pin, the release, the layer, the objects it
// produces. That only holds while the two agree: a chart declared in a file
// named after another is a chart nobody finds by looking, and a second
// declaration in one file is the thing this replaced.
func TestChartFiles_AreOnePerChart(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob(filepath.Join("..", "..", chartsPackage, "*.go"))
	require.NoError(t, err)
	require.NotEmpty(t, files)

	var declared []string

	for _, path := range files {
		name := filepath.Base(path)
		if strings.HasSuffix(name, "_test.go") || notAChart[name] {
			continue
		}

		key := strings.TrimSuffix(name, ".go")
		declared = append(declared, key)

		raw, readErr := os.ReadFile(path)
		require.NoError(t, readErr, path)

		assert.Equal(t, 1, strings.Count(string(raw), "register(Definition{"),
			"%s must declare exactly one chart: its own", name)

		_, known := charts.Lookup(key)
		assert.NoError(t, known,
			"%s declares no chart called %q — the file name is the registry key", name, key)
	}

	registered := charts.Keys()

	slices.Sort(declared)
	assert.Equal(t, registered, declared,
		"the files in %s and the registry disagree: a chart declared in a file named "+
			"something else is one nobody finds by looking for it", chartsPackage)
}

// TestChartsPackage_StaysALeaf keeps the declarations cheap to read.
//
// Everything imports this package: the gates here, internal/pkg/cni,
// internal/pkg/layer, the policy pack, and five command-line tools. It reaches
// internal/pkg/platform and nothing else, and the day it reaches Pulumi's SDK
// every one of those grows by 768 packages — the same failure
// TestClusterSpec_PullsNoPulumi exists for, and the reason the values data and
// the render helpers stayed where they are.
func TestChartsPackage_StaysALeaf(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	reached := map[string]bool{}
	require.NoError(t, walkImports(t, root, Module+chartsPackage, reached))

	require.NotEmpty(t, reached, "no imports were read, so this proved nothing")

	for imported := range reached {
		assert.False(t, strings.HasPrefix(imported, pulumiPrefix),
			"%s reaches %s. Every gate and every tool that names a chart now links Pulumi's "+
				"SDK, and a chart declaration is not the place to pay for that",
			chartsPackage, imported)
	}
}
