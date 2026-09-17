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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// devTaskfile is the second entry point, and the flag every workflow passes.
const devTaskfile = "Taskfile.dev.yaml"

// includeName matches one include's name in an `includes:` block.
var includeName = regexp.MustCompile(`(?m)^  ([a-z][a-z0-9-]*):`)

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
// library modules that act on live servers and the three local taskfiles that
// describe a cluster. Taskfile.dev.yaml reaches all five through `iac:`.
var rootIncludes = []string{"hcloud", "helm", "cluster", "platform", "policy"}

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

	assert.ElementsMatch(t, rootIncludes, names(includesOf(t, body)),
		"the root taskfile includes something other than the taskfiles that describe a "+
			"cluster. The checks live in %s", devTaskfile)

	// The tasks themselves, without the comments: the file talks about the
	// checks on purpose, saying where they went.
	_, tasks, found := strings.Cut(body, "\ntasks:\n")
	require.True(t, found, "the root taskfile declares no tasks")

	for name, taskBody := range tasksIn("\ntasks:\n" + tasks) {
		stripped := withoutComments(taskBody)

		for _, tool := range checkTools {
			assert.NotContains(t, stripped, tool,
				"the root task `%s` runs %s, which asks a question about this repository "+
					"rather than about a cluster. It belongs in %s", name, tool, devTaskfile)
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

	raw, err := os.ReadFile(filepath.Join("..", "..", devTaskfile))
	require.NoError(t, err)

	includes := includesOf(t, string(raw))
	require.NotEmpty(t, includes, "%s includes nothing", devTaskfile)

	require.True(t, includes["iac"],
		"%s has no `iac:` include, so anything it needs from the root taskfile is a copy",
		devTaskfile)

	for _, name := range rootIncludes {
		assert.False(t, includes[name],
			"%s includes %q directly instead of reaching it through `iac:`, which gives that "+
				"taskfile two readers and nothing holding them equal", devTaskfile, name)
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

				assert.Equal(t, []string{"-t", devTaskfile}, args[:min(2, len(args))],
					"%s runs `task %s` without -t %s, so it resolves against the root "+
						"taskfile, which holds no check:\n  %s",
					name, args[0], devTaskfile, trimmed)
			}
		}
	}

	assert.Positive(t, checked, "no workflow runs any task; this test is checking nothing")
}

// taskInvocation matches `task` and the arguments that follow it on one line.
// `task:` as a YAML key does not match: a colon follows the word directly.
var taskInvocation = regexp.MustCompile(`\btask((?:[ \t]+[^ \t|)]+)+)`)

// includesOf returns the names in a taskfile's `includes:` block.
func includesOf(t *testing.T, body string) map[string]bool {
	t.Helper()

	_, includes, found := strings.Cut(body, "\nincludes:\n")
	require.True(t, found, "the taskfile has no includes block")

	includes, _, _ = strings.Cut(includes, "\ntasks:\n")

	names := map[string]bool{}
	for _, match := range includeName.FindAllStringSubmatch(includes, -1) {
		names[match[1]] = true
	}

	return names
}

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

	root := filepath.Join("..", "..")

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
			if entry == devTaskfile {
				want = devTaskfile
			}

			assert.Equal(t, want, flag,
				"%s says `task %s`, and that task is on %s. Pasted as written it does not run",
				filepath.Base(path), name, entry)
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

	for _, entry := range []string{"Taskfile.yaml", devTaskfile} {
		raw, err := os.ReadFile(filepath.Join(root, entry))
		require.NoError(t, err, entry)

		body := string(raw)

		for _, found := range declaredTask.FindAllStringSubmatch(body, -1) {
			owner[found[1]] = entry
		}

		for name := range includesOf(t, body) {
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

	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "commands.md"))
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
			if strings.HasPrefix(row[1], "-t "+devTaskfile) {
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
			section, root, dev, devTaskfile)
	}

	assert.Positive(t, checked, "no task table was found, so this test proved nothing")
}
