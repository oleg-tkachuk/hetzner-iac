package ci

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"
)

// nameDefinition is the one file allowed to write the repository's name.
const nameDefinition = "internal/pkg/clusterspec/name.go"

// modulePath is how the name appears in every import line, and is not a second
// definition of it: `go.mod` owns that, and clusterspec.Name deliberately does
// not derive from it — see the comment there.
const modulePath = "github.com/oleg-tkachuk/hetzner-iac"

// nameLiteralsWithAReason are the files that may spell the name out, and why.
//
// Each one is a place Go cannot reach: a YAML document read by another tool, or
// a fixture that exists to be compared against what the constant produces.
var nameLiteralsWithAReason = map[string]string{
	"policy/PulumiPolicy.yaml": "the pack's manifest, read by the Pulumi CLI before any Go runs. " +
		"TestPolicyPack_IsNamedOnce holds it equal to the constant",
	"infra/cluster/cluster.example.yaml": "an example topology, which must carry the apiVersion a " +
		"real one carries — it is the document the schema accepts, not a reference to it",
	"internal/pkg/clusterspec/clusterspectest/clusterspectest.go": "the shared topology fixture, " +
		"for the same reason: it is a document",
	"Taskfile.yaml": "the log prefix every task prints, and PROJECT_NAME beside it. Cosmetic: a " +
		"wrong label costs a confusing line of output, not a resource",
	"Taskfile.dev.yaml": "the same log prefix, in the second entry point",
	"infra/cluster/cluster.schema.json": "the JSON Schema an editor validates a topology against " +
		"as it is typed. TestTopologySchema_PinsTheApiVersionThatGoAccepts holds it equal",
}

// TestProjectName_IsWrittenInOnePlace is the whole point of clusterspec.Name.
//
// Five contracts are built from that string and each pair of them has to agree
// exactly: the component type tokens that every URN carries, the `group:`
// prefix tools/target matches them by, the `managed-by` label the orphan check
// selects on, the apiVersion a topology is validated against, and the policy
// pack's name. Nothing compares them at run time, and the four failures are not
// alike — a renamed token orphans resources, a renamed label makes billed
// resources invisible, a renamed apiVersion fails loudly, and the pack's name
// is cosmetic.
//
// So the constant is not tidiness: it is the only place that spread is visible
// at once.
func TestProjectName_IsWrittenInOnePlace(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	var checked int

	for _, path := range tracked(t, root) {
		switch {
		case path == nameDefinition:
			continue
		case strings.HasSuffix(path, ".md"), strings.HasSuffix(path, "go.mod"), strings.HasSuffix(path, "go.sum"):
			// Prose names the repository constantly, and the module files are
			// where the import path belongs.
			continue
		case strings.HasSuffix(path, "_test.go"):
			// A test may hold a URN or a topology taken off a real stack. That
			// is measured evidence, and rewriting it through the constant would
			// make the fixture agree with the code by construction — which is
			// the one thing a fixture must not do.
			continue
		}

		if !namesTheProject(t, root, path) {
			continue
		}

		checked++

		reason, allowed := nameLiteralsWithAReason[path]
		assert.True(t, allowed,
			"%s writes %q out. It is built from clusterspec.Name in %s, because the type tokens, "+
				"the group prefix, the managed-by label, the apiVersion and the pack name must "+
				"agree and nothing checks that at run time — import the constant, or add the "+
				"file here with the reason it cannot",
			path, clusterspec.Name, nameDefinition)

		if allowed {
			assert.NotEmpty(t, reason, path)
		}
	}

	assert.Positive(t, checked, "no file named the project at all, so this test proved nothing")
}

// TestPolicyPack_IsNamedOnce holds the manifest the CLI reads equal to the
// constant the program passes.
//
// Two halves of one name, in two languages: `pulumi preview --policy-pack`
// reads the YAML, and the pack the Go program builds carries the other. They
// are not compared anywhere at run time.
func TestPolicyPack_IsNamedOnce(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "policy", "PulumiPolicy.yaml"))
	require.NoError(t, err)

	var declared string

	for _, line := range strings.Split(string(raw), "\n") {
		if after, found := strings.CutPrefix(line, "name:"); found {
			declared = strings.TrimSpace(after)

			break
		}
	}

	assert.Equal(t, clusterspec.Name, declared,
		"PulumiPolicy.yaml names the pack %q and the program names it %q",
		declared, clusterspec.Name)
}

// namesTheProject reports whether a file writes the project's name where it matters.
//
// For Go it reads STRING LITERALS from the syntax tree rather than the file's
// text, because comments name the repository constantly and legitimately —
// tools/target explains `group:` by quoting a URN, and policy/main.go says
// which pack it builds. A mention is not a use, which is the same distinction
// internal/ci/tiers_test.go draws for a provider call.
//
// Everything else is read as text, because YAML and JSON have no other form.
func namesTheProject(t *testing.T, root, path string) bool {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		return false
	}

	if !strings.HasSuffix(path, ".go") {
		return strings.Contains(strings.ReplaceAll(string(raw), modulePath, ""), clusterspec.Name)
	}

	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, path), raw, 0)
	require.NoError(t, err, path)

	found := false

	ast.Inspect(file, func(node ast.Node) bool {
		literal, ok := node.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}

		if strings.Contains(strings.ReplaceAll(literal.Value, modulePath, ""), clusterspec.Name) {
			found = true

			return false
		}

		return true
	})

	return found
}

// TestTopologySchema_PinsTheApiVersionThatGoAccepts is the other half of the
// document contract.
//
// An editor validates a topology against the schema as it is typed; the program
// validates it again with clusterspec. A schema pinning an apiVersion the Go
// code rejects is the worst of the two failures available here: the file looks
// right in the editor and fails at apply time.
func TestTopologySchema_PinsTheApiVersionThatGoAccepts(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "infra", "cluster", "cluster.schema.json"))
	require.NoError(t, err)

	assert.Contains(t, string(raw), `"const": "`+clusterspec.Name+`/v1"`,
		"the schema does not pin apiVersion to %s/v1, so an editor and the program disagree "+
			"about what a topology is", clusterspec.Name)
}

// dataStorageClassVar matches the retaining class's name in the root taskfile.
var dataStorageClassVar = regexp.MustCompile(`DATA_STORAGE_CLASS:\s*(\S+)`)

// TestDestroyPrompt_NamesTheRetainingClass holds the prompt's copy of the
// class name equal to the constant, and holds the prompt to naming it at all.
//
// The prompt is the only place a teardown says what SURVIVES, and a volume on
// the retaining class does: it outlives the cluster and keeps being billed
// until somebody deletes it by hand. A prompt cannot import Go, so the name is
// written twice — and the failure of a drift is not an error but an operator
// who tore a cluster down believing nothing was left.
func TestDestroyPrompt_NamesTheRetainingClass(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "Taskfile.yaml"))
	require.NoError(t, err)

	declared := dataStorageClassVar.FindStringSubmatch(string(raw))
	require.NotNil(t, declared, "no DATA_STORAGE_CLASS in the root taskfile")

	assert.Equal(t, platform.StorageClassDatabase, declared[1],
		"the taskfile names %q and internal/pkg/platform names %q",
		declared[1], platform.StorageClassDatabase)

	prompt := destroyPrompt.FindString(string(raw))
	require.NotEmpty(t, prompt, "no destroy prompt in the root taskfile")

	assert.Contains(t, prompt, "{{.DATA_STORAGE_CLASS}}",
		"the destroy prompt does not say that volumes on the retaining class survive")
}

// destroyPrompt matches the root destroy task's prompt, which is one folded
// scalar ending at the next key.
var destroyPrompt = regexp.MustCompile(`(?s)prompt: >-\n.*?Irreversible`)
