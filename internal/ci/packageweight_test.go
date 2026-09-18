package ci

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Module is this repository's import path prefix.
const Module = "github.com/oleg-tkachuk/hetzner-iac/"

// componentPackage builds cloud resources and needs a provider SDK to do it.
const componentPackage = Module + "internal/pkg/hetzner"

// specPackage is the same cluster as a document rather than as a resource, and
// the whole point of it is that it costs nothing to read.
const specPackage = Module + "internal/pkg/clusterspec"

// pulumiPrefix matches every package of Pulumi's SDK, providers included.
const pulumiPrefix = "github.com/pulumi/"

// TestClusterSpec_PullsNoPulumi holds the line that makes the command-line
// tools cheap.
//
// Measured before this package existed, when the topology lived beside the
// component resources: `tools/topology`, which validates a YAML file, linked
// 816 packages into a 44 MB binary. It is 84 packages and 4.7 MB now, and so
// are `tools/stack`, `tools/talos`, `tools/secrets` and `tools/recoverykit`.
//
// One import undoes all of it, and nothing about the failure would say so — the
// build stays green, the tests stay green, and five binaries quietly grow by a
// factor of nine. The weight is not the provider SDKs, which are three packages
// each; it is Pulumi's own SDK underneath them, which is 768.
func TestClusterSpec_PullsNoPulumi(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	reached := map[string]bool{}
	require.NoError(t, walkImports(t, root, specPackage, reached))

	require.NotEmpty(t, reached, "no imports were read, so this proved nothing")

	for imported := range reached {
		assert.False(t, strings.HasPrefix(imported, pulumiPrefix),
			"%s reaches %s. Every program that only reads a topology now links Pulumi's "+
				"SDK again — 768 packages, and five binaries that were 4.7 MB are 44 MB. "+
				"Whatever needed it belongs in %s",
			specPackage, imported, componentPackage)
	}
}

// TestTools_DoNotImportTheComponentPackage keeps the command-line tools on the
// document side of the split.
//
// Nine of them imported it, and not one used a resource from it: they wanted a
// topology, a label, a machine-config patch or a token. What they got as well
// was the hcloud, Talos and Kubernetes provider SDKs.
func TestTools_DoNotImportTheComponentPackage(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	entries, err := os.ReadDir(filepath.Join(root, "tools"))
	require.NoError(t, err)

	var checked int

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		checked++

		imports, importErr := directImports(t, filepath.Join(root, "tools", entry.Name()))
		require.NoError(t, importErr, entry.Name())

		assert.NotContains(t, imports, componentPackage,
			"tools/%s imports %s. A command-line program that wants a topology, a label or a "+
				"patch should import %s: the component package exists to declare cloud resources, "+
				"and importing it links a provider SDK to read a file",
			entry.Name(), componentPackage, specPackage)
	}

	assert.Positive(t, checked, "no tool directories found; this test is checking nothing")
}

// walkImports follows this repository's own packages from one of them,
// collecting everything reachable.
//
// Only this module's packages are followed — a third-party package's own
// imports are its business, and what matters here is whether OUR code reaches
// Pulumi, not how deep somebody else's dependency tree goes.
func walkImports(t *testing.T, root, pkg string, reached map[string]bool) error {
	t.Helper()

	imports, err := directImports(t, filepath.Join(root, strings.TrimPrefix(pkg, Module)))
	if err != nil {
		return err
	}

	for _, imported := range imports {
		if reached[imported] {
			continue
		}

		reached[imported] = true

		if strings.HasPrefix(imported, Module) {
			if err := walkImports(t, root, imported, reached); err != nil {
				return err
			}
		}
	}

	return nil
}

// directImports is every package one directory's Go files import, tests
// excluded: a test's imports are not linked into anything that ships.
//
// One ParseFile per file rather than ParseDir, which is deprecated because it
// ignores build tags. Reading the files directly is what is wanted here
// anyway: an import is a cost whichever tag admits the file that carries it.
func directImports(t *testing.T, dir string) ([]string, error) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	set := token.NewFileSet()

	var found []string

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, parseErr := parser.ParseFile(set, filepath.Join(dir, name), nil, parser.ImportsOnly)
		if parseErr != nil {
			return nil, parseErr
		}

		for _, imported := range file.Imports {
			found = append(found, strings.Trim(imported.Path.Value, `"`))
		}
	}

	return found, nil
}
