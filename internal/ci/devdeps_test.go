package ci

// The dependency tasks on the dev entry point, and the tidiness gate.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/ci/citest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// goGetTool moves the modules behind go.mod's `tool` directives. The library's
// `go:deps:update` does not: it bumps direct requirements, and a tool's module
// is an indirect one.
const goGetTool = "go get tool"

// devTasks returns every task Taskfile.dev.yaml declares, comments removed.
func devTasks(t *testing.T) map[string]string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("..", "..", citest.DevTaskfile))
	require.NoError(t, err)

	tasks := tasksIn(string(raw))
	require.NotEmpty(t, tasks, "%s declares no tasks", citest.DevTaskfile)

	for name, body := range tasks {
		tasks[name] = withoutComments(body)
	}

	return tasks
}

// TestDevTaskfile_DependencyTasks holds what each dependency task runs.
func TestDevTaskfile_DependencyTasks(t *testing.T) {
	t.Parallel()

	tasks := devTasks(t)

	tests := []struct {
		task string
		runs []string
	}{
		{task: "tidy-check", runs: []string{"go mod tidy -diff", "go mod verify"}},
		{task: "deps:outdated", runs: []string{"task: go:deps:outdated", "task: charts:outdated"}},
		{task: "deps:update", runs: []string{goGetTool, "task: go:deps:update"}},
		{task: "verify", runs: []string{"task: tidy-check"}},
	}

	for _, tt := range tests {
		t.Run(tt.task, func(t *testing.T) {
			t.Parallel()

			body, found := tasks[tt.task]
			require.True(t, found, "%s has no `%s` task", citest.DevTaskfile, tt.task)

			for _, want := range tt.runs {
				assert.Contains(t, body, want, "`%s` no longer runs %q", tt.task, want)
			}
		})
	}
}

// TestDevTaskfile_DepsUpdateBumpsToolsFirst keeps `go get tool` ahead of the
// library task, whose tidy and build then cover what it moved. After it, a
// tool bump would reach go.mod untidied and unbuilt.
func TestDevTaskfile_DepsUpdateBumpsToolsFirst(t *testing.T) {
	t.Parallel()

	body := devTasks(t)["deps:update"]

	tools := strings.Index(body, goGetTool)
	library := strings.Index(body, "task: go:deps:update")

	require.NotEqual(t, -1, tools, "deps:update does not run %q", goGetTool)
	require.NotEqual(t, -1, library, "deps:update does not run go:deps:update")
	assert.Less(t, tools, library, "deps:update runs %q after go:deps:update", goGetTool)
}
