package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTopology puts body at dir/name and returns the path.
func writeTopology(t *testing.T, dir, name, body string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	return path
}

func TestRun_ValidTopologiesPrintOkAndPass(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeTopology(t, dir, "cluster.prod.yaml", validTopology)

	var stdout, stderr bytes.Buffer

	invalid, err := run([]string{dir}, &stdout, &stderr)

	require.NoError(t, err)
	assert.False(t, invalid)
	assert.Equal(t, "ok  "+path+"\n", stdout.String())
	assert.Empty(t, stderr.String())
	assert.Equal(t, exitValid, exitCode(invalid))
}

func TestRun_InvalidTopologyIsReportedOnStderrAndFails(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	good := writeTopology(t, dir, "cluster.good.yaml", validTopology)
	bad := writeTopology(t, dir, "cluster.bad.yaml", "not: a topology\n")

	var stdout, stderr bytes.Buffer

	invalid, err := run([]string{dir}, &stdout, &stderr)

	require.NoError(t, err)
	assert.True(t, invalid)
	assert.Equal(t, "ok  "+good+"\n", stdout.String())
	assert.Contains(t, stderr.String(), bad+":\n    ")
	assert.NotContains(t, stderr.String(), good)
	assert.Equal(t, exitInvalid, exitCode(invalid))
}

func TestRun_ChecksEveryDirectoryNamed(t *testing.T) {
	t.Parallel()

	first, second := t.TempDir(), t.TempDir()
	one := writeTopology(t, first, "cluster.dev.yaml", validTopology)
	two := writeTopology(t, second, "cluster.prod.yaml", validTopology)

	var stdout bytes.Buffer

	invalid, err := run([]string{first, second}, &stdout, &bytes.Buffer{})

	require.NoError(t, err)
	assert.False(t, invalid)
	assert.Equal(t, "ok  "+one+"\nok  "+two+"\n", stdout.String())
}

func TestRun_DefaultsToTheRepositoryDirectory(t *testing.T) {
	t.Parallel()

	// Run from this package's directory, the default resolves to a path that
	// is not there; the error naming it is what pins the default.
	_, err := run(nil, &bytes.Buffer{}, &bytes.Buffer{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), `directory "infra/cluster" does not exist`)
}

func TestRun_NothingToCheckIsAnError(t *testing.T) {
	t.Parallel()

	invalid, err := run([]string{t.TempDir()}, &bytes.Buffer{}, &bytes.Buffer{})

	require.ErrorIs(t, err, ErrNoTopologies)
	assert.False(t, invalid)
}

func TestRun_GetPrintsTheField(t *testing.T) {
	t.Parallel()

	path := writeTopology(t, t.TempDir(), "cluster.prod.yaml", validTopology)

	var stdout, stderr bytes.Buffer

	invalid, err := run([]string{getCommand, "cluster-name", path}, &stdout, &stderr)

	require.NoError(t, err)
	assert.False(t, invalid)
	assert.Equal(t, "platform-test\n", stdout.String())
	assert.Empty(t, stderr.String())
}

func TestRun_GetPrintsNothingOnARefusal(t *testing.T) {
	t.Parallel()

	path := writeTopology(t, t.TempDir(), "cluster.prod.yaml", validTopology)

	var stdout bytes.Buffer

	_, err := run([]string{getCommand, "no-such-field", path}, &stdout, &bytes.Buffer{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown field "no-such-field"`)
	assert.Empty(t, stdout.String())
}

func TestRun_GetWithTheWrongArgumentCountIsADirectoryList(t *testing.T) {
	t.Parallel()

	// `get` is a subcommand only with exactly two arguments after it; any
	// other count is directories to validate, as it always was.
	_, err := run([]string{getCommand, "talos-version"}, &bytes.Buffer{}, &bytes.Buffer{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), `directory "get" does not exist`)
}
