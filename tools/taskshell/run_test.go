package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errBoom = errors.New("boom")

// repository is a root holding every taskfile in Paths, each with content.
func repository(t *testing.T, content string) string {
	t.Helper()

	root := t.TempDir()

	for _, path := range Paths {
		require.NoError(t, os.MkdirAll(filepath.Join(root, filepath.Dir(path)), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(root, path), []byte(content), 0o600))
	}

	return root
}

// shellcheckSays is a check that reports found, and records what it was given.
func shellcheckSays(found []Finding, given *[]Taskfile) func(context.Context, []Taskfile) ([]Finding, error) {
	return func(_ context.Context, files []Taskfile) ([]Finding, error) {
		*given = files

		return found, nil
	}
}

func testDeps(root string, check func(context.Context, []Taskfile) ([]Finding, error), out *bytes.Buffer) deps {
	return deps{root: func(context.Context) (string, error) { return root, nil }, check: check, out: out}
}

// The fixture holds six blocks, and every one of the seven taskfiles is it.
func TestRun_SaysHowMuchItCheckedWhenShellcheckIsQuiet(t *testing.T) {
	t.Parallel()

	var (
		out   bytes.Buffer
		given []Taskfile
	)

	require.NoError(t, run(context.Background(), nil, testDeps(repository(t, fixture), shellcheckSays(nil, &given), &out)))

	assert.Len(t, given, len(Paths), "every taskfile reaches shellcheck")
	assert.Equal(t, "42 shell blocks in 7 taskfiles, no shellcheck findings\n", out.String())
}

func TestRun_PrintsEveryFindingAndFails(t *testing.T) {
	t.Parallel()

	var (
		out   bytes.Buffer
		given []Taskfile
	)

	finding := Finding{Block: Block{Taskfile: Paths[0], Task: "blocks"}, Line: 8, Column: 6, Level: "info", Code: 2086,
		Message: "Double quote to prevent globbing and word splitting."}

	err := run(context.Background(), nil, testDeps(repository(t, fixture), shellcheckSays([]Finding{finding}, &given), &out))
	require.Error(t, err)
	assert.Equal(t, "1 shellcheck findings in 42 shell blocks across 7 taskfiles", err.Error())
	assert.Equal(t, finding.String()+"\n", out.String())
}

func TestRun_RefusesToCheckNothing(t *testing.T) {
	t.Parallel()

	var (
		out   bytes.Buffer
		given []Taskfile
	)

	quiet := shellcheckSays(nil, &given)

	require.Error(t, run(context.Background(), []string{"extra"}, testDeps(t.TempDir(), quiet, &out)))

	err := run(context.Background(), nil, testDeps(repository(t, "version: \"3\"\n"), quiet, &out))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no shell found in any taskfile")

	err = run(context.Background(), nil, testDeps(t.TempDir(), quiet, &out))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read "+Paths[0])

	err = run(context.Background(), nil, testDeps(repository(t, "tasks: ["), quiet, &out))
	require.Error(t, err)

	unrooted := testDeps("", quiet, &out)
	unrooted.root = func(context.Context) (string, error) { return "", errBoom }
	require.ErrorIs(t, run(context.Background(), nil, unrooted), errBoom)

	failing := testDeps(repository(t, fixture), func(context.Context, []Taskfile) ([]Finding, error) { return nil, errBoom }, &out)
	require.ErrorIs(t, run(context.Background(), nil, failing), errBoom)

	assert.Nil(t, given, "shellcheck never ran")
	assert.Empty(t, out.String())
}
