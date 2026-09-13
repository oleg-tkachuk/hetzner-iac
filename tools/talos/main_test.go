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
		// The committed template counts, so cluster:config-check validates
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
