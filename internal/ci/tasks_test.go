package ci

// What more than one of the taskfile gates needs: where the taskfiles are,
// and how to cut one into its tasks.
//
// It was one file with four subjects in it — 1 032 lines, and the reason three
// checks were appended to the wrong place in a day: the helpers were there, so
// everything went there. The subjects are taskguards, taskdocs, taskoutput and
// clustertasks now, and this is what they share.

import (
	"regexp"
	"strings"
)

// taskName matches a task declaration at the top of a taskfile's tasks block.
var taskName = regexp.MustCompile(`^  ([a-zA-Z_][\w:-]*):\s*$`)

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
