package ci

// The documentation against the tasks: every task a document tells somebody to
// run exists, and none of them is one the library include leaves out.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// documentedTask matches a task named in prose or a table, with or without the
// verb: `task cluster:plan`, and a bare `cluster:etcd:upload`.
//
// The verb was once required, and that is how nine renamed names survived in
// backticks alone. Dropping it means Pulumi config keys — `hcloud:token`,
// `ingress:loadBalancerType` — match the pattern too; ownsNamespace below is
// what separates them, because no taskfile declares an `hcloud:` or `ingress:`
// namespace.
var documentedTask = regexp.MustCompile("`(?:task )?([a-z][a-z0-9-]*(?::[a-z0-9-]+)+)")

// declaredTask matches a task declaration inside a taskfile.
var declaredTask = regexp.MustCompile(`(?m)^  ([a-z][a-z0-9:_-]*):\s*$`)

// TestDocs_NameOnlyTasksThatExist guards the one documentation error that
// wastes an operator's time rather than merely misleading them: a command
// they copy, paste and watch fail.
//
// It is not hypothetical. Renaming a task is a two-file edit that looks like
// one, and this repository has renamed several — cluster:power-status became
// hcloud:servers in the same change that moved it. Nothing else compares the
// two sides.
//
// Included tasks come from the shared library and are not declared here, so
// their names are checked against `task --list` being available rather than
// against a file. Anything namespaced by an include that this repository does
// not declare is skipped: the alternative is running Task from a test.
func TestDocs_NameOnlyTasksThatExist(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	declared := map[string]bool{}

	for _, path := range taskfiles(t) {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)

		// Neither entry point namespaces its own tasks: `task plan` and
		// `task -t Taskfile.dev.yaml verify` are both bare. Only the files
		// under tasks/ are included under a name.
		namespace := ""
		if base := filepath.Base(path); strings.HasSuffix(base, ".task.yaml") {
			namespace = strings.TrimSuffix(base, ".task.yaml") + ":"
		}

		for _, found := range declaredTask.FindAllStringSubmatch(string(raw), -1) {
			declared[namespace+found[1]] = true
		}
	}

	require.NotEmpty(t, declared, "no tasks found to compare against")

	var checked int

	for _, path := range documents(t, root) {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)

		for _, found := range documentedTask.FindAllStringSubmatch(string(raw), -1) {
			name := found[1]

			// A namespace this repository does not own belongs to the shared
			// library, whose tasks are not in any file here.
			if namespace, _, found := strings.Cut(name, ":"); found && !ownsNamespace(declared, namespace) {
				continue
			}

			checked++

			assert.True(t, declared[name],
				"%s names %q, which no taskfile declares", filepath.Base(path), name)
		}
	}

	assert.Positive(t, checked, "no documented task names found; the pattern has drifted")
}

// ownsNamespace reports whether this repository declares any task in it.
func ownsNamespace(declared map[string]bool, namespace string) bool {
	for name := range declared {
		if strings.HasPrefix(name, namespace+":") {
			return true
		}
	}

	return false
}

// libraryExclude is one library task this repository refuses, and where.
type libraryExclude struct {
	entryPoint string
	include    string
	task       string
	because    string
}

// libraryExcludes are the exclusions whose failure is silent.
//
// `excludes` naming a task the module does not have removes nothing, and says
// nothing: the task list simply grows by one. The helm entry is the measured
// case — the task was `uninstall-all` until taskfiles v6.0.0 renamed it, so
// the bump that carried the rename would have quietly handed this repository a
// helm uninstall it must not have.
var libraryExcludes = []libraryExclude{
	{
		entryPoint: "Taskfile.yaml",
		include:    "helm",
		task:       "uninstall",
		because: "every Helm release on this cluster is created by Pulumi. Uninstalling one " +
			"with Helm leaves Pulumi state claiming it exists, so the next `up` reports no " +
			"changes while the cluster is empty",
	},
	{
		entryPoint: devTaskfile,
		include:    "checkov",
		task:       "baseline",
		because: "a baseline accepts every current finding at once with nothing saying why " +
			"any of them stands, and this repository accepts findings the other way — " +
			"skip-check in .checkov.yaml, each with its reason beside it",
	},
}

// TestLibraryExcludes_StayInPlace holds each of them.
func TestLibraryExcludes_StayInPlace(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	for _, excluded := range libraryExcludes {
		t.Run(excluded.include+":"+excluded.task, func(t *testing.T) {
			t.Parallel()

			raw, err := os.ReadFile(filepath.Join(root, excluded.entryPoint))
			require.NoError(t, err, excluded.entryPoint)

			// The include block, so the assertion is about that module rather
			// than about the word appearing anywhere in the file.
			block := regexp.MustCompile(`(?ms)^  ` + excluded.include + `:\n(.*?)(?:^  [a-z#])`).
				FindStringSubmatch(string(raw))
			require.Len(t, block, 2, "no %s include in %s", excluded.include, excluded.entryPoint)

			// Anchored to the end of the line, not `\b`: a word boundary sits
			// between `uninstall` and the hyphen in `uninstall-all`, so the
			// obvious pattern matches the stale name it exists to reject.
			// Caught by trying to make this test fail.
			assert.Regexp(t, `(?m)excludes:[\s\S]*?- `+excluded.task+` *$`, block[1],
				"the %s include no longer excludes %s, or excludes a name the module does "+
					"not have — %s", excluded.include, excluded.task, excluded.because)
		})
	}
}

// includeWithExcludes matches an include and its body, which is where a
// library task this repository refuses is named.
var includeWithExcludes = regexp.MustCompile(`(?m)^  ([a-z][a-z0-9-]*):\n((?:    .*\n|\n)+)`)

// excludedTask matches one entry of an `excludes:` list.
var excludedTask = regexp.MustCompile(`(?m)^      - ([a-z:_-]+)\s*$`)

// TestDocs_NameNoExcludedTask closes the hole the other documentation gate
// leaves open by design.
//
// That gate skips any namespace this repository does not declare, because the
// included library's tasks live in another repository — so every `task
// security:…` mention goes unchecked. The excludes list is the part of that
// namespace we DO know: a task named there is not available here, and a
// document telling somebody to run it is wrong in a way nothing else catches.
//
// Measured: the Brewfile installed hadolint "# task security:dockerfile", and
// `dockerfile` is excluded because this repository ships no Dockerfile. The
// formula was dead weight and the line read as instruction.
func TestDocs_NameNoExcludedTask(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	excluded := map[string]bool{}

	// Both entry points: the go and security modules are included by the dev
	// taskfile and carry most of the excludes, so reading the root alone would
	// leave every `task security:…` mention in the documentation unchecked —
	// which is the hole this test exists to close.
	for _, name := range []string{"Taskfile.yaml", devTaskfile} {
		raw, err := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, err, name)

		_, includes, found := strings.Cut(string(raw), "\nincludes:\n")
		require.True(t, found, "%s has no includes block", name)

		includes, _, _ = strings.Cut(includes, "\ntasks:\n")

		for _, include := range includeWithExcludes.FindAllStringSubmatch(includes, -1) {
			for _, entry := range excludedTask.FindAllStringSubmatch(include[2], -1) {
				excluded[include[1]+":"+entry[1]] = true
			}
		}
	}

	require.NotEmpty(t, excluded, "no excluded task was found, so this test proved nothing")

	paths, err := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	require.NoError(t, err)

	records, err := filepath.Glob(filepath.Join(root, "docs", "adr", "*.md"))
	require.NoError(t, err)

	community, err := filepath.Glob(filepath.Join(root, ".github", "*.md"))
	require.NoError(t, err)

	paths = append(paths, records...)
	paths = append(paths, community...)
	paths = append(paths,
		filepath.Join(root, "README.md"),
		filepath.Join(root, "ROADMAP.md"),
		filepath.Join(root, "Brewfile"))

	for _, path := range paths {
		if filepath.Base(path) == "BACKLOG.md" {
			continue
		}

		text, readErr := os.ReadFile(path)
		require.NoError(t, readErr, path)

		for name := range excluded {
			assert.NotContains(t, string(text), "task "+name,
				"%s names task %s, which this repository excludes from the library include",
				filepath.Base(path), name)
		}
	}
}

// documents is every file that tells somebody to run a task: the
// documentation, the records, the community files, the Brewfile and the
// README.
//
// Shared with TestDocs_NameTheEntryPointThatHasTheTask, because a second copy
// of this list is how one of the two checks silently stops reading a file.
// Each entry below is here because something stale survived in it.
func documents(t *testing.T, root string) []string {
	t.Helper()

	docs, err := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	require.NoError(t, err)

	// The records too. Leaving them out is how `task cluster:image-bake`
	// survived the colon rename: the stale name was only in docs/adr, and the
	// glob above stops at docs/.
	records, err := filepath.Glob(filepath.Join(root, "docs", "adr", "*.md"))
	require.NoError(t, err)

	docs = append(docs, records...)

	// The community files too — SECURITY.md and CONTRIBUTING.md live under
	// .github rather than docs, and are read by people who have never run a
	// task here.
	community, err := filepath.Glob(filepath.Join(root, ".github", "*.md"))
	require.NoError(t, err)

	docs = append(docs, community...)

	// And the Brewfile, which is prose about tasks even though it is not
	// Markdown. Leaving it out is how `task cluster:config-check` survived in
	// it — a task that has never existed under that name, in the one file a
	// new clone reads before anything else works.
	docs = append(docs, filepath.Join(root, "Brewfile"), filepath.Join(root, "README.md"))

	kept := make([]string, 0, len(docs))

	for _, path := range docs {
		// BACKLOG.md is planning, and planning names the task it wants
		// before that task exists — which is the point of writing it down.
		// It is gitignored for the same reason it is skipped here: it is not
		// documentation an operator copies from. On a clean clone the glob
		// never finds it.
		if filepath.Base(path) == "BACKLOG.md" {
			continue
		}

		kept = append(kept, path)
	}

	require.NotEmpty(t, kept, "no documents found, so nothing was checked")

	return kept
}
