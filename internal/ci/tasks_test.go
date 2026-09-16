package ci

// What more than one of the taskfile gates needs: where the taskfiles are,
// and how to cut one into its tasks.
//
// It was one file with four subjects in it — 1 032 lines, and the reason three
// checks were appended to the wrong place in a day: the helpers were there, so
// everything went there. The subjects are taskguards, taskdocs, taskoutput and
// clustertasks now, and this is what they share.

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// taskName matches a task declaration at the top of a taskfile's tasks block.
var taskName = regexp.MustCompile(`^  ([a-zA-Z_][\w:-]*):\s*$`)

// taskfiles is every taskfile in the repository: the root one and each
// include.
//
// Derived from the directory rather than listed, because both tests below
// walked a hardcoded list of three — so a new taskfile would not have been
// checked and nothing would have said so. That is the failure mode these
// tests exist to prevent, one level up.
func taskfiles(t *testing.T) []string {
	t.Helper()

	root := filepath.Join("..", "..")

	includes, err := filepath.Glob(filepath.Join(root, "tasks", "*.task.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, includes, "no taskfiles under tasks/")

	return append([]string{filepath.Join(root, "Taskfile.yaml")}, includes...)
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
