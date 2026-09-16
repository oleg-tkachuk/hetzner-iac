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

// outputConstant matches a stack-output constant and captures the name it
// publishes. The same shape in a layer and in internal/pkg/clusterref, which is what
// lets one rule cover both producers.
var outputConstant = regexp.MustCompile(`(?m)^\tOutput\w+\s+= "(\w+)"$`)

// exportCall matches a stack export. The capture is whatever was passed as the
// name: a constant identifier, or a string literal.
var exportCall = regexp.MustCompile(`Ctx\.Export\(([^,]+),`)

// shellReadsOutput matches the two ways a taskfile names an output: reading one
// directly, and holding one in a var for jq to read out of the JSON.
var shellReadsOutput = regexp.MustCompile(`stack output (\w+)|^  _[A-Z_]*OUT_[A-Z_]+: (\w+)$`)

// producers are every Pulumi project that exports, and the contract package
// the cluster tier's names live in.
func producers(t *testing.T) []string {
	t.Helper()

	root := filepath.Join("..", "..")

	// Every Go file in each project, not main.go alone. A layer that grows a
	// second file — layers/10-node-platform splits its create functions and
	// its chart data out — would otherwise take its output constants with it
	// and leave this check reading a file that no longer holds them, which is
	// a pass that proves nothing.
	var paths []string

	for _, pattern := range []string{
		filepath.Join(root, "layers", "*", "*.go"),
		filepath.Join(root, "infra", "cluster", "*.go"),
	} {
		matched, err := filepath.Glob(pattern)
		require.NoError(t, err)

		for _, path := range matched {
			if !strings.HasSuffix(path, "_test.go") {
				paths = append(paths, path)
			}
		}
	}

	require.NotEmpty(t, paths, "no layer sources found")

	return append(paths, filepath.Join(root, "internal", "pkg", "clusterref", "clusterref.go"))
}

// TestLayers_ExportOnlyNamedOutputs is the convention, and it is deliberately
// uniform: every stack output is a named constant, in every project.
//
// Two layers named theirs and two wrote literals, with no rule saying which —
// so the next export was a coin toss. Uniform rather than "name the ones a
// machine reads", because who reads an output changes: `ingressIp` is read by
// a person today and by whatever writes DNS records tomorrow, and a rename at
// that point is a rename in two languages.
//
// It also makes the set greppable. `grep Output internal/pkg/clusterref layers` is the
// whole list of what this repository publishes.
func TestLayers_ExportOnlyNamedOutputs(t *testing.T) {
	t.Parallel()

	var checked int

	for _, path := range producers(t) {
		raw, err := os.ReadFile(path)
		require.NoError(t, err, path)

		for _, match := range exportCall.FindAllStringSubmatch(string(raw), -1) {
			name := strings.TrimSpace(match[1])

			checked++

			assert.False(t, strings.HasPrefix(name, `"`),
				"%s exports %s as a literal; declare it as an Output constant so the "+
					"published set is greppable and a consumer can import the name",
				relativeToRoot(path), name)
		}
	}

	assert.Positive(t, checked, "no exports found; this test is checking nothing")
}

// TestOutputs_ReadByShellAreDeclaredInGo holds the contract that crosses a
// language boundary, for every consumer that crosses it.
//
// A taskfile cannot import a constant, so it spells the name again. The
// failure when the two drift is not an error: `pulumi stack output <gone>`
// prints nothing, jq answers `null`, and the task reports the layer as
// unapplied — pointing the operator at an apply that will not fix it.
//
// This replaces a test that checked the backup layer only. The tier's
// kubeconfig and talosconfig cross the same boundary and were guarded a
// different way — by pinning the constant's value inside internal/pkg/clusterref, which
// freezes the Go side without ever reading the shell side. Both are kept: the
// pin catches a rename, this catches a rename the other half did not follow.
func TestOutputs_ReadByShellAreDeclaredInGo(t *testing.T) {
	t.Parallel()

	declared := map[string]string{}

	for _, path := range producers(t) {
		raw, err := os.ReadFile(path)
		require.NoError(t, err, path)

		for _, match := range outputConstant.FindAllStringSubmatch(string(raw), -1) {
			declared[match[1]] = relativeToRoot(path)
		}
	}

	require.NotEmpty(t, declared, "no output constants found; this test is checking nothing")

	var checked int

	for _, path := range taskfiles(t) {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)

		for _, line := range strings.Split(string(raw), "\n") {
			for _, match := range shellReadsOutput.FindAllStringSubmatch(line, -1) {
				name := match[1]
				if name == "" {
					name = match[2]
				}

				checked++

				_, found := declared[name]
				assert.True(t, found,
					"%s reads the stack output %q and no Go constant publishes that name",
					relativeToRoot(path), name)
			}
		}
	}

	assert.Positive(t, checked, "no taskfile reads a stack output by name; this test is checking nothing")
}

// relativeToRoot trims the ../.. these tests read through, so a failure names
// a path the way the repository does.
func relativeToRoot(path string) string {
	return strings.TrimPrefix(filepath.ToSlash(path), "../../")
}
