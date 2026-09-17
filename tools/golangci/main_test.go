package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPinIn_ReadsTheVersionCIInstalls holds the parser to the shape of the
// workflow rather than to a fixture, because the pin is read from the file CI
// installs from and a second copy is what this exists to avoid.
func TestPinIn_ReadsTheVersionCIInstalls(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		workflow string
		want     string
		wantErr  string
	}{
		"quoted with a leading v": {
			workflow: "env:\n  GOLANGCI_VERSION: \"v2.13.2\"\n", want: "2.13.2",
		},
		"unquoted and without one": {
			workflow: "env:\n  GOLANGCI_VERSION: 2.13.2\n", want: "2.13.2",
		},
		"beside other pins": {
			workflow: "env:\n  GOSEC_VERSION: \"v2.29.0\"\n  GOLANGCI_VERSION: \"v2.13.2\"\n",
			want:     "2.13.2",
		},
		"no env at all": {
			workflow: "on:\n  push:\n", wantErr: "nothing says which version CI runs",
		},
		"env without the pin": {
			workflow: "env:\n  GOSEC_VERSION: \"v2.29.0\"\n", wantErr: "nothing says which version CI runs",
		},
		"not yaml": {
			workflow: "\tenv:\n\t\tbroken\n", wantErr: "parse the workflow",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := pinIn([]byte(test.workflow))

			if test.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), test.wantErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

// TestPinIn_ReadsTheRealWorkflow is the other half: the parser above can be
// right about a fixture and wrong about the file it will actually read.
func TestPinIn_ReadsTheRealWorkflow(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", workflow))
	require.NoError(t, err)

	pinned, err := pinIn(raw)
	require.NoError(t, err)

	assert.Regexp(t, `^\d+\.\d+\.\d+$`, pinned,
		"the pin read from the workflow is not a version, so the comparison would always fail")
}

// TestVersionIn_ReadsWhatTheBinaryPrints covers the formats this has seen,
// including the one that started it: a locally built binary whose provenance
// fields are all unknown.
func TestVersionIn_ReadsWhatTheBinaryPrints(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		output  string
		want    string
		wantErr bool
	}{
		"a release build": {
			output: "golangci-lint has version 2.13.2 built with go1.27.0 from 27774aa on 2026-08-27T22:52:01Z",
			want:   "2.13.2",
		},
		"a go install build": {
			output: `golangci-lint has version 2.12.2 built with go1.27.1 from (unknown, modified: ?, mod sum: "h1:7+d1") on (unknown)`,
			want:   "2.12.2",
		},
		"with a leading v": {
			output: "golangci-lint has version v2.13.2 built with go1.27.0", want: "2.13.2",
		},
		"something else entirely": {
			output: "command not found", wantErr: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := versionIn(test.output)

			if test.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

// TestResolve_PrefersTheRepositorysOwnCopy is the behaviour the whole tool is
// for: a pinned binary in bin/ wins over whatever PATH offers.
func TestResolve_PrefersTheRepositorysOwnCopy(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "bin"), 0o750))

	local := filepath.Join(root, LocalBinary)
	require.NoError(t, os.WriteFile(local, []byte("#!/bin/sh\n"), 0o700))

	resolved, err := resolve(root)
	require.NoError(t, err)
	assert.Equal(t, local, resolved, "bin/ exists and executable, and PATH was used anyway")
}

// TestResolve_IgnoresSomethingThatCannotRun keeps a half-finished download
// from being treated as the linter: an interrupted curl leaves a file that is
// there and not executable.
func TestResolve_IgnoresSomethingThatCannotRun(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "bin"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, LocalBinary), []byte("partial"), 0o600))

	resolved, err := resolve(root)
	if err != nil {
		// No golangci-lint on this PATH either, which is a valid environment
		// and the error says what to do about it.
		assert.Contains(t, err.Error(), LocalBinary)

		return
	}

	assert.NotEqual(t, filepath.Join(root, LocalBinary), resolved,
		"a file with no execute bit was chosen as the linter")
}
