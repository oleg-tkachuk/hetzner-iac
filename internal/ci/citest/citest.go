// Package citest holds what internal/ci and internal/ci/docs both read the
// same way: the tracked files, the taskfiles, and the Pulumi projects.
//
// A separate package because the gates live in two: the documentation gates
// run on every pull request and must compile without the rest, and a second
// copy of a helper is how one of two checks silently stops reading a file.
// It takes *testing.T, so it stays out of anything that ships, as layertest
// does.
//
// Every function takes the repository root, because the two packages sit at
// different depths.
package citest

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// DevTaskfile is the second entry point, and the flag every workflow passes.
const DevTaskfile = "Taskfile.dev.yaml"

// PulumiPrefix matches every package of Pulumi's SDK, providers included.
const PulumiPrefix = "github.com/pulumi/"

// Tracked is every file git knows about, which is what "published" means.
//
// Read from git rather than walked from disk on purpose: the gitignored files
// are the ones holding an operator's real values — the topology with their
// admin CIDR, the stack config with their token — and a sweep that read those
// would refuse the very files that are allowed to hold them.
func Tracked(t *testing.T, root string) []string {
	t.Helper()

	// #nosec G204 -- git with literal arguments and the caller's repository
	// root, passed as a vector: there is no shell to interpret any of it.
	listed, err := exec.CommandContext(t.Context(), "git", "-C", root, "ls-files").Output()
	require.NoError(t, err)

	return strings.Split(strings.TrimSpace(string(listed)), "\n")
}

// Taskfiles is every taskfile in the repository: both entry points and each
// include.
//
// Derived from the directory rather than listed, because the tests walked a
// hardcoded list of three — so a new taskfile would not have been checked and
// nothing would have said so.
func Taskfiles(t *testing.T, root string) []string {
	t.Helper()

	includes, err := filepath.Glob(filepath.Join(root, "tasks", "*.task.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, includes, "no taskfiles under tasks/")

	return append([]string{
		filepath.Join(root, "Taskfile.yaml"),
		filepath.Join(root, DevTaskfile),
	}, includes...)
}

// LayerList matches the whitespace-separated LAYERS list in the root
// taskfile, which the apply loop walks.
var LayerList = regexp.MustCompile(`(?s)LAYERS: >-\n((?:    [^\n]+\n)+)`)

// includeName matches one include's name in an `includes:` block.
var includeName = regexp.MustCompile(`(?m)^  ([a-z][a-z0-9-]*):`)

// IncludesOf returns the names in a taskfile's `includes:` block.
func IncludesOf(t *testing.T, body string) map[string]bool {
	t.Helper()

	_, includes, found := strings.Cut(body, "\nincludes:\n")
	require.True(t, found, "the taskfile has no includes block")

	// Not an error: without a tasks block the includes run to the end of the
	// file, and the whole remainder is what is wanted.
	includes, _, _ = strings.Cut(includes, "\ntasks:\n")

	names := map[string]bool{}
	for _, match := range includeName.FindAllStringSubmatch(includes, -1) {
		names[match[1]] = true
	}

	return names
}
