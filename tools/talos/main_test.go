package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractTag(t *testing.T) {
	t.Parallel()

	// talosctl prints the version differently depending on the flags, so the
	// parser scans for the token rather than matching a layout.
	for name, output := range map[string]string{
		"short form": "Client:\nTalos v1.13.10",
		"long form":  "Client:\n\tTag:         v1.13.10\n\tSHA:         undefined",
		"bare":       "v1.13.10",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, "v1.13.10", extractTag(output))
		})
	}
}

func TestExtractTag_NoVersion(t *testing.T) {
	t.Parallel()

	// An empty result must be distinguishable, because the caller turns it
	// into a clear error rather than comparing against nothing.
	assert.Empty(t, extractTag(""))
	assert.Empty(t, extractTag("command not found"))
}

func TestMinor(t *testing.T) {
	t.Parallel()

	// Talos moves configuration between documents across minors, not patches,
	// so the comparison is deliberately at minor granularity: 1.13.0 and
	// 1.13.10 validate the same way, 1.13 and 1.14 do not.
	for in, want := range map[string]string{
		"v1.13.10": "v1.13",
		"v1.13.0":  "v1.13",
		"1.13.10":  "v1.13",
		"v1.14.0":  "v1.14",
	} {
		assert.Equal(t, want, minor(in), in)
	}

	assert.NotEqual(t, minor("v1.13.10"), minor("v1.14.0"),
		"a minor mismatch is what the guard exists to catch")
	assert.Equal(t, minor("v1.13.0"), minor("v1.13.10"),
		"a patch difference must not block the check")
}

func TestMinor_Malformed(t *testing.T) {
	t.Parallel()

	// Returned unchanged rather than guessed at: a version this cannot parse
	// will not match the pinned one, and the caller reports both.
	assert.Equal(t, "nonsense", minor("nonsense"))
}

func TestIsTopologyFile(t *testing.T) {
	t.Parallel()

	// Mirrors the name infra/cluster/main.go derives from the stack.
	for name, want := range map[string]bool{
		"cluster.prod.yaml":     true,
		"cluster.dev.yaml":      true,
		"cluster.yaml":          false,
		"cluster.prod.yml":      false,
		"cluster.prod.old.yaml": false,
		"Pulumi.yaml":           false,
		// The committed template counts, so cluster:machine-config:check validates
		// it against talosctl too — a broken example is found here rather
		// than by whoever copies it.
		"cluster.example.yaml": true,
	} {
		assert.Equal(t, want, isTopologyFile(name), name)
	}
}

func TestIndent(t *testing.T) {
	t.Parallel()

	// Talos error output is multi-line; indenting it keeps the tool's own
	// message distinguishable from what Talos said.
	assert.Equal(t, "      one\n      two", indent("one\ntwo\n"))
}

func TestTopologyFiles_ListsOnlyStackTopologies(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	for _, name := range []string{
		"cluster.dev.yaml",
		"cluster.prod.yaml",
		"Pulumi.yaml",
		"main.go",
		"cluster.dev.yaml.bak",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600))
	}

	require.NoError(t, os.Mkdir(filepath.Join(dir, "cluster.nested.yaml"), 0o750))

	paths, err := topologyFiles(dir)

	require.NoError(t, err)
	assert.Equal(t, []string{
		filepath.Join(dir, "cluster.dev.yaml"),
		filepath.Join(dir, "cluster.prod.yaml"),
	}, paths, "a directory named like a topology is not one")

	// Sorted, because os.ReadDir sorts: the order stacks are validated in is
	// the order they are reported in, and a set would make the output move
	// between runs.
	assert.IsNonDecreasing(t, paths)
}

func TestTopologyFiles_RefusesADirectoryWithNoStacks(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("x"), 0o600))

	_, err := topologyFiles(dir)

	// Zero files checked must not read as a clean validation: running this
	// from the repository root would otherwise report success.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no cluster.<stack>.yaml files")
}

func TestTopologyFiles_NamesADirectoryItCannotRead(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "nowhere")

	_, err := topologyFiles(missing)

	require.Error(t, err)
	assert.Contains(t, err.Error(), missing)
}

// TestTalosctlPath_PrefersTheRepositorysOwn is the fix for a check nobody
// could run.
//
// Homebrew carries one talosctl, the newest. This check needs the minor the
// topology pins, and refuses anything else — so on any machine where `brew
// install talosctl` had been run, it declined, which is every fresh clone.
// bin/talosctl is the way out, and it only helps if it is preferred over PATH.
func TestTalosctlPath_PrefersTheRepositorysOwn(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	require.NoError(t, os.MkdirAll(filepath.Dir(LocalTalosctl), 0o750))
	require.NoError(t, os.WriteFile(LocalTalosctl, []byte("#!/bin/sh\nexit 0\n"), 0o700))

	chosen, err := talosctlPath()
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(dir, LocalTalosctl), chosen,
		"a talosctl in %s must win over PATH, or installing the pinned one changes nothing",
		LocalTalosctl)
	assert.True(t, filepath.IsAbs(chosen), "the path is handed to exec and must not depend on the cwd")
}

// TestTalosctlPath_IgnoresOneItCannotRun keeps the preference narrow.
//
// A file at that path with no execute bit is a half-finished download, not a
// binary. Choosing it would replace "talosctl is the wrong version" — which
// names its own fix — with a permission error from exec.
func TestTalosctlPath_IgnoresOneItCannotRun(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	require.NoError(t, os.MkdirAll(filepath.Dir(LocalTalosctl), 0o750))
	require.NoError(t, os.WriteFile(LocalTalosctl, []byte("not a binary"), 0o600))

	chosen, err := talosctlPath()
	if err != nil {
		// No talosctl on this machine's PATH either, which is the other
		// half of the contract: the error names the task that fixes it.
		assert.Contains(t, err.Error(), "cluster:talosctl:install")

		return
	}

	assert.NotEqual(t, filepath.Join(dir, LocalTalosctl), chosen,
		"a file with no execute bit was chosen, and exec will fail on it")
}

// TestTalosctlPath_DirectoryIsNotABinary covers the other way that path can
// exist without being usable: `mkdir -p bin/talosctl`, which a mistyped task
// would leave behind.
func TestTalosctlPath_DirectoryIsNotABinary(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	require.NoError(t, os.MkdirAll(LocalTalosctl, 0o750))

	chosen, err := talosctlPath()
	if err != nil {
		assert.Contains(t, err.Error(), "cluster:talosctl:install")

		return
	}

	assert.NotEqual(t, filepath.Join(dir, LocalTalosctl), chosen)
}
