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

// entryPoint is the `-t Taskfile.dev.yaml` a check carries, in a documented
// command and in a workflow step alike. Optional: the cluster tasks are on the
// root entry point and carry nothing.
const entryPoint = `(?:-t \S+ )?`

// gateMarker is the dagger docs/commands.md puts after a task a workflow runs,
// in a table row: `| `+"`task -t Taskfile.dev.yaml fmt-check`"+` † | … |`.
var gateMarker = regexp.MustCompile("(?m)^\\| `task " + entryPoint + "([a-z][a-z0-9:-]*)`[^|]*† \\|")

// taskRow is any task the reference documents, marked or not.
var taskRow = regexp.MustCompile("(?m)^\\| `task " + entryPoint + "([a-z][a-z0-9:-]*)`")

// workflowInvocation matches a task a workflow runs. Comment lines are dropped
// before this is applied: the workflows talk about tasks in prose constantly —
// "the same task an operator runs", "a new task is a real feat" — and every
// one of those is in a comment.
var workflowInvocation = regexp.MustCompile(`\btask ` + entryPoint + `([a-z][a-z0-9:-]*)`)

// TestGateMarkers_MatchTheWorkflows holds the dagger in docs/commands.md equal
// to the tasks the workflows actually invoke.
//
// The marker tells a reader which commands they never have to type, so it is
// read as a statement about CI. Both ways of being wrong are quiet: a task CI
// runs and the reference does not mark reads as one more thing to remember,
// and a marked task CI does not run is a check somebody believes is covered.
//
// Only .github/workflows is read. A composite action could run a task too,
// and none does — while `description:` fields there are prose that is not a
// comment, so scanning them would demand a marker for a sentence.
func TestGateMarkers_MatchTheWorkflows(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	reference := filepath.Join("docs", "commands.md")

	raw, err := os.ReadFile(filepath.Join(root, reference))
	require.NoError(t, err)

	documented := map[string]bool{}
	for _, match := range taskRow.FindAllStringSubmatch(string(raw), -1) {
		documented[match[1]] = true
	}

	require.NotEmpty(t, documented, "%s documents no tasks; this test is checking nothing", reference)

	marked := map[string]bool{}
	for _, match := range gateMarker.FindAllStringSubmatch(string(raw), -1) {
		marked[match[1]] = true
	}

	run := tasksTheWorkflowsRun(t, root)
	require.NotEmpty(t, run, "no workflow runs any task; this test is checking nothing")

	for task := range run {
		// A task the reference does not list at all is another test's
		// business — taskargs_test.go holds that list — and saying it twice
		// here would report one gap as two.
		if !documented[task] {
			continue
		}

		assert.True(t, marked[task],
			"a workflow runs `task %s` and %s does not mark it with †, so it reads as a "+
				"command somebody has to remember to run", task, reference)
	}

	for task := range marked {
		assert.True(t, run[task],
			"%s marks `task %s` with † and no workflow runs it, so a check nobody performs "+
				"reads as covered", reference, task)
	}
}

// tasksTheWorkflowsRun returns the task names invoked in .github/workflows,
// ignoring comments.
func tasksTheWorkflowsRun(t *testing.T, root string) map[string]bool {
	t.Helper()

	dir := filepath.Join(root, ".github", "workflows")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err, dir)

	run := map[string]bool{}

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

			for _, match := range workflowInvocation.FindAllStringSubmatch(trimmed, -1) {
				run[match[1]] = true
			}
		}
	}

	return run
}
