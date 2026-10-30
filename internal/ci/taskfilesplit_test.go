package ci

// The two task entry points, and the line between them.
//
// `task` with no arguments is the list an operator reads, so it holds the
// cluster operations and nothing else; `task -t Taskfile.dev.yaml` holds the
// checks, the scanners, the formatters and the chart pins. Both halves of that
// split fail quietly when they erode, which is what these hold.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/ci/citest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// checkTools are the tools that answer a question about this repository rather
// than about a cluster. The root taskfile runs none of them.
//
// Named as tools rather than as task names because that is the invariant: a
// check can be added under any name, and it still has to reach for one of
// these to do anything. `go test` is deliberately absent — `task e2e` runs the
// e2e suite against a live cluster, which is a cluster operation.
var checkTools = []string{
	"golangci-lint",
	"gosec",
	"gitleaks",
	"trivy",
	"checkov",
	"lychee",
	"govulncheck",
	"gofmt",
	"kubeconform",
	"zizmor",
	"actionlint",
}

// rootIncludes is every include the root entry point may declare: the two
// library modules that act on live servers and the local taskfiles that
// describe a cluster and the tiers beside it. Taskfile.dev.yaml reaches them
// all through `iac:`.
var rootIncludes = []string{"hcloud", "helm", "backup", "cluster", "platform", "policy"}

// TestRootTaskfile_HoldsNoCheck keeps the rule that put these two files apart
// from eroding back into one.
//
// It is a rule about the command line, not about tidiness. A clone that only
// wants to apply a cluster should not need a pipeline's configuration to do
// it — two of the check tasks read a pinned version out of
// .github/workflows/ci.yaml — and an operator reading `task` should not have
// to tell a scanner apart from a teardown. Before the split a third of that
// list was work they never run.
//
// Adding a convenient check back to the root is a one-line edit that looks
// harmless and nothing else notices: the list simply grows by one.
func TestRootTaskfile_HoldsNoCheck(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	raw, err := os.ReadFile(filepath.Join(root, "Taskfile.yaml"))
	require.NoError(t, err)

	body := string(raw)

	assert.ElementsMatch(t, rootIncludes, names(citest.IncludesOf(t, body)),
		"the root taskfile includes something other than the taskfiles that describe a "+
			"cluster. The checks live in %s", citest.DevTaskfile)

	// The tasks themselves, without the comments: the file talks about the
	// checks on purpose, saying where they went.
	_, tasks, found := strings.Cut(body, "\ntasks:\n")
	require.True(t, found, "the root taskfile declares no tasks")

	for name, taskBody := range tasksIn("\ntasks:\n" + tasks) {
		stripped := withoutComments(taskBody)

		for _, tool := range checkTools {
			assert.NotContains(t, stripped, tool,
				"the root task `%s` runs %s, which asks a question about this repository "+
					"rather than about a cluster. It belongs in %s", name, tool, citest.DevTaskfile)
		}

		assert.NotContains(t, stripped, ".github/",
			"the root task `%s` reads something under .github/, so applying a cluster now "+
				"needs a pipeline's configuration", name)
	}
}

// TestDevTaskfile_RedeclaresNoIaCInclude keeps one definition per taskfile.
//
// Taskfile.dev.yaml needs two things the root declares — the machine-config
// validation and the layer list CI builds its matrix from — and it reaches
// both through the `iac:` include. Including tasks/cluster.task.yaml here
// instead would work, and would make LAYERS and the layer enum drift from a
// second reader nothing holds to the first.
func TestDevTaskfile_RedeclaresNoIaCInclude(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", citest.DevTaskfile))
	require.NoError(t, err)

	includes := citest.IncludesOf(t, string(raw))
	require.NotEmpty(t, includes, "%s includes nothing", citest.DevTaskfile)

	require.True(t, includes["iac"],
		"%s has no `iac:` include, so anything it needs from the root taskfile is a copy",
		citest.DevTaskfile)

	for _, name := range rootIncludes {
		assert.False(t, includes[name],
			"%s includes %q directly instead of reaching it through `iac:`, which gives that "+
				"taskfile two readers and nothing holding them equal", citest.DevTaskfile, name)
	}
}

// TestWorkflows_CallTasksThroughTheDevTaskfile is the half of the split that
// fails silently.
//
// Task finds Taskfile.yaml by itself and has no environment variable for the
// second file, so every workflow step naming a task has to pass `-t`. Forget
// it and the step resolves against the root entry point, which either fails
// with "does not exist" — the loud case — or, for a name both files declare,
// runs the wrong one.
func TestWorkflows_CallTasksThroughTheDevTaskfile(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	dir := filepath.Join(root, ".github", "workflows")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err, dir)

	var checked int

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || (!strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml")) {
			continue
		}

		raw, readErr := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, readErr, name)

		for _, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				continue
			}

			for _, match := range taskInvocation.FindAllStringSubmatch(trimmed, -1) {
				args := strings.Fields(match[1])
				require.NotEmpty(t, args)

				checked++

				assert.Equal(t, []string{"-t", citest.DevTaskfile}, args[:min(2, len(args))],
					"%s runs `task %s` without -t %s, so it resolves against the root "+
						"taskfile, which holds no check:\n  %s",
					name, args[0], citest.DevTaskfile, trimmed)
			}
		}
	}

	assert.Positive(t, checked, "no workflow runs any task; this test is checking nothing")
}

// taskInvocation matches `task` and the arguments that follow it on one line.
// `task:` as a YAML key does not match: a colon follows the word directly.
var taskInvocation = regexp.MustCompile(`\btask((?:[ \t]+[^ \t|)]+)+)`)

// names is the include names as a slice, so a set comparison reports which
// one is extra or missing rather than a boolean.
func names(set map[string]bool) []string {
	got := make([]string, 0, len(set))
	for name := range set {
		got = append(got, name)
	}

	return got
}

// yamlComment matches a whole-line YAML comment inside a task body.
var yamlComment = regexp.MustCompile(`(?m)^\s*#.*$`)

// withoutComments drops the prose, so an assertion about what a task RUNS is
// not answered by a sentence explaining why it does not.
//
// This is not hypothetical here: the root taskfile's own comments name the
// checks and say where they went, which is exactly the text a naive search
// would trip over.
func withoutComments(body string) string {
	return yamlComment.ReplaceAllString(body, "")
}
