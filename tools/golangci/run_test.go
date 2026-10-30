package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errBoom = errors.New("boom")

const (
	pinnedVersion = "2.13.2"
	findingsExit  = 1
)

// checkout is a repository root with the pin and an executable bin/ copy.
func checkout(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, filepath.Dir(workflow)), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, workflow),
		[]byte("env:\n  "+pinKey+": v"+pinnedVersion+"\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(root, filepath.Dir(LocalBinary)), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, LocalBinary), []byte("#!/bin/sh\n"), 0o700))

	return root
}

// linted records the run it was asked for.
type linted struct {
	binary, root string
	args         []string
}

func testDeps(root, installed string, code int, lintErr error, got *linted) deps {
	return deps{
		root:    func(context.Context) (string, error) { return root, nil },
		version: func(context.Context, string) (string, error) { return installed, nil },
		lint: func(_ context.Context, binary, dir string, args []string) (int, error) {
			*got = linted{binary: binary, root: dir, args: args}

			return code, lintErr
		},
	}
}

func TestRun_LintsWithThePinnedCopyAndPassesItsExitCodeOn(t *testing.T) {
	t.Parallel()

	root := checkout(t)

	var got linted

	code, err := run(context.Background(), []string{"run", "./..."}, testDeps(root, pinnedVersion, findingsExit, nil, &got))
	require.NoError(t, err)
	assert.Equal(t, findingsExit, code, "a finding stays a finding")
	assert.Equal(t, linted{binary: filepath.Join(root, LocalBinary), root: root, args: []string{"run", "./..."}}, got)
}

func TestRun_RefusesAnotherVersionThanCIs(t *testing.T) {
	t.Parallel()

	var got linted

	_, err := run(context.Background(), nil, testDeps(checkout(t), "2.12.2", 0, nil, &got))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is 2.12.2 and CI pins "+pinnedVersion)
	assert.Empty(t, got.binary, "the wrong linter never ran")
}

func TestRun_NamesWhatItCouldNotDo(t *testing.T) {
	t.Parallel()

	var got linted

	_, err := run(context.Background(), nil, testDeps(checkout(t), pinnedVersion, 0, errBoom, &got))
	require.ErrorIs(t, err, errBoom)
	assert.Contains(t, err.Error(), "run "+got.binary)

	_, err = run(context.Background(), nil, testDeps(t.TempDir(), pinnedVersion, 0, nil, &got))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read the pin")

	unrooted := testDeps("", pinnedVersion, 0, nil, &got)
	unrooted.root = func(context.Context) (string, error) { return "", errBoom }
	_, err = run(context.Background(), nil, unrooted)
	require.ErrorIs(t, err, errBoom)

	unasked := testDeps(checkout(t), pinnedVersion, 0, nil, &got)
	unasked.version = func(context.Context, string) (string, error) { return "", errBoom }
	_, err = run(context.Background(), nil, unasked)
	require.ErrorIs(t, err, errBoom)
}
