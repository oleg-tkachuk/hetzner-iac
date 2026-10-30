package docs

// Documented commands against the two entry points: each names the one that
// has the task, and the command reference keeps them in separate tables. The
// split itself is held in internal/ci/taskfilesplit_test.go.

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

// documentedCommand matches a command a document tells somebody to RUN: the
// verb, the entry point when the task needs one, and the name.
//
// The verb is required here, unlike in documentedTask, and that is the whole
// point: a bare `charts:validate` in prose is a reference to a task, while
// `task charts:validate` is a line somebody pastes into a terminal.
//
// Two forms, because a command reaches a reader two ways. The first is inline
// code; the second is a fenced or indented block, where there is no backtick
// to anchor on — which is where `task go:test` survived the first pass of
// this gate.
var documentedCommands = []*regexp.Regexp{
	regexp.MustCompile("`task (-t (\\S+) )?([a-z][a-z0-9:-]*)"),
	regexp.MustCompile(`(?m)^[ \t]*\$?[ \t]*task (-t (\S+) )?([a-z][a-z0-9:-]*)`),
}

// TestDocs_NameTheEntryPointThatHasTheTask keeps every documented command
// paste-able.
//
// A name that exists is not enough once there are two entry points. `task
// charts:validate` names a task that is declared, and fails — the root
// entry point does not include the charts taskfile, and Task answers "does
// not exist" for a command the documentation states in full. The reverse is
// quieter and worse: `task -t Taskfile.dev.yaml cluster:plan` reaches an
// internal include and is refused, so the documentation hands over a command
// that cannot run.
//
// Nine lines were wrong the moment the split landed — in ci.md, design.md,
// the README, the Brewfile and tools/golangci — and every one of them named a
// task that exists.
func TestDocs_NameTheEntryPointThatHasTheTask(t *testing.T) {
	t.Parallel()

	root := repoRoot

	owner := taskOwners(t, root)
	require.NotEmpty(t, owner, "no task could be traced to an entry point")

	var checked int

	for _, path := range documents(t, root) {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)

		var found [][]string
		for _, pattern := range documentedCommands {
			found = append(found, pattern.FindAllStringSubmatch(string(raw), -1)...)
		}

		for _, match := range found {
			flag, name := match[2], match[3]

			entry, known := owner[name]
			if !known {
				// A library task, whose names live in the other repository.
				// The namespace is still ours: it names the entry point that
				// includes the module.
				namespace, _, hasNamespace := strings.Cut(name, ":")
				if !hasNamespace {
					continue
				}

				if entry, known = owner[namespace+":"]; !known {
					continue
				}
			}

			checked++

			want := ""
			if entry == citest.DevTaskfile {
				want = citest.DevTaskfile
			}

			assert.Equal(t, want, flag,
				"%s says `task %s`, and that task is on %s. Pasted as written it does not run",
				relativeToRoot(path), name, entry)
		}
	}

	assert.Positive(t, checked, "no documented command was traced to an entry point")
}

// taskOwners maps a task to the entry point that can run it: every task name
// an entry point declares itself, plus every namespace it includes, so a
// library task whose name lives in another repository is covered by its
// prefix.
func taskOwners(t *testing.T, root string) map[string]string {
	t.Helper()

	owner := map[string]string{}

	for _, entry := range []string{"Taskfile.yaml", citest.DevTaskfile} {
		raw, err := os.ReadFile(filepath.Join(root, entry))
		require.NoError(t, err, entry)

		body := string(raw)

		for _, found := range declaredTask.FindAllStringSubmatch(body, -1) {
			owner[found[1]] = entry
		}

		for name := range citest.IncludesOf(t, body) {
			// The root entry point as the dev one sees it. Its tasks are typed
			// on the root, never through this include: it is internal, so
			// `task -t Taskfile.dev.yaml iac:…` is refused.
			if name == "iac" {
				continue
			}

			owner[name+":"] = entry

			// And the tasks of a local include, by name, because those are
			// declared in this repository.
			included := filepath.Join(root, "tasks", name+".task.yaml")

			nested, readErr := os.ReadFile(included)
			if readErr != nil {
				continue
			}

			for _, found := range declaredTask.FindAllStringSubmatch(string(nested), -1) {
				owner[name+":"+found[1]] = entry
			}
		}
	}

	return owner
}

// tableHeading matches a section heading in the command reference, and
// commandRow one row of a task table.
var (
	tableHeading = regexp.MustCompile(`(?m)^#{2,3} (.+)$`)
	commandRow   = regexp.MustCompile("(?m)^\\| `task ([^`]+)`")
)

// TestCommandReference_KeepsTheEntryPointsInSeparateTables holds the reference
// readable.
//
// A table whose rows are half `task …` and half `task -t Taskfile.dev.yaml …`
// reads as inconsistency rather than as two entry points, and the flag stops
// carrying information: a reader who sees it on some rows and not others has
// to check each one. One table, one entry point, and the heading above it says
// which.
//
// It happened in the same change that introduced the split: the Layers table
// gained a dev-side command in a row's description.
func TestCommandReference_KeepsTheEntryPointsInSeparateTables(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join(repoRoot, "docs", "commands.md"))
	require.NoError(t, err)

	body := string(raw)

	headings := tableHeading.FindAllStringSubmatchIndex(body, -1)
	require.NotEmpty(t, headings, "the command reference has no sections")

	var checked int

	for i, heading := range headings {
		end := len(body)
		if i+1 < len(headings) {
			end = headings[i+1][0]
		}

		section := body[heading[2]:heading[3]]

		var root, dev int

		for _, row := range commandRow.FindAllStringSubmatch(body[heading[1]:end], -1) {
			if strings.HasPrefix(row[1], "-t "+citest.DevTaskfile) {
				dev++
			} else {
				root++
			}
		}

		if root+dev == 0 {
			continue
		}

		checked++

		assert.Zero(t, min(root, dev),
			"the %q table mixes the two entry points — %d cluster command(s) and %d on %s. "+
				"Split it, so the flag on a row means something",
			section, root, dev, citest.DevTaskfile)
	}

	assert.Positive(t, checked, "no task table was found, so this test proved nothing")
}
