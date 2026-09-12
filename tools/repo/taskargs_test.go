package repo

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// taskName matches a task declaration at the top of a taskfile's tasks block.
var taskName = regexp.MustCompile(`^  ([a-zA-Z_][\w:-]*):\s*$`)

// stackGuard is the precondition that turns a forgotten stack= into a usage
// message instead of a command aimed at nothing.
const stackGuard = `- sh: '[ -n "{{.stack}}" ]'`

// TestTasks_ThatNeedAStackSaySoWhenItIsMissing closes the gap left by removing
// the dev default.
//
// The default was the danger: `task platform:destroy-all` with a forgotten
// stack= meant dev, and the once that is wrong is the once it matters. With no
// default and no guard, the same command would instead reach Pulumi with an
// empty --stack and fail somewhere less obvious.
//
// A task whose dependency carries the guard is exempt, because Task runs
// dependencies before a task's own preconditions: the dependency's guard fires
// first, so a second one there would be unreachable.
func TestTasks_ThatNeedAStackSaySoWhenItIsMissing(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	files := []string{
		filepath.Join(root, "Taskfile.yaml"),
		filepath.Join(root, "tasks", "cluster.task.yaml"),
		filepath.Join(root, "tasks", "platform.task.yaml"),
	}

	var checked int

	for _, path := range files {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)

		tasks := tasksIn(string(raw))

		for name, body := range tasks {
			if !needsStack(body) {
				continue
			}

			checked++

			if strings.Contains(body, stackGuard) {
				continue
			}

			// No guard of its own is allowed only when a dependency carries
			// one: Task runs dependencies before a task's own preconditions,
			// so the dependency's guard fires first and a second one here
			// would be unreachable.
			//
			// Resolved rather than assumed. Exempting anything with a `deps:`
			// would let a task depend on an unguarded one and reach Pulumi
			// with an empty --stack.
			deps := dependenciesOf(body)
			require.NotEmpty(t, deps,
				"%s in %s reads the stack, has no guard, and depends on nothing that could carry one",
				name, filepath.Base(path))

			for _, dep := range deps {
				assert.Contains(t, tasks[dep], stackGuard,
					"%s in %s relies on %s for its stack guard, and %s has none",
					name, filepath.Base(path), dep, dep)
			}
		}
	}

	assert.Positive(t, checked, "no task reads a stack — this test is checking nothing")
}

// dependencies matches the inline list form this repository uses.
var dependencies = regexp.MustCompile(`deps: \[([^\]]+)\]`)

// dependenciesOf names the tasks a body declares as dependencies.
func dependenciesOf(body string) []string {
	match := dependencies.FindStringSubmatch(body)
	if match == nil {
		return nil
	}

	var out []string

	for _, dep := range strings.Split(match[1], ",") {
		out = append(out, strings.TrimSpace(dep))
	}

	return out
}

// needsStack reports whether a task body reads the stack in any of its
// spellings.
func needsStack(body string) bool {
	for _, spelling := range []string{"._CL_STACK", "._PL_STACK", "{{.stack}}"} {
		if strings.Contains(body, spelling) {
			return true
		}
	}

	return false
}

// tasksIn splits a taskfile's tasks block into one body per task.
func tasksIn(text string) map[string]string {
	_, after, found := strings.Cut(text, "\ntasks:\n")
	if !found {
		return nil
	}

	tasks := map[string]string{}

	var (
		current string
		body    strings.Builder
	)

	flush := func() {
		if current != "" {
			tasks[current] = body.String()
		}

		body.Reset()
	}

	for _, line := range strings.Split(after, "\n") {
		if match := taskName.FindStringSubmatch(line); match != nil {
			flush()

			current = match[1]

			continue
		}

		body.WriteString(line + "\n")
	}

	flush()

	return tasks
}
