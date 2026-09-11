package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFactoryArchitecture_TranslatesBothAndRefusesTheRest(t *testing.T) {
	t.Parallel()

	// Talos says x86 and arm; the factory says amd64 and arm64. The `case`
	// statement this replaces fell through to an error for anything else,
	// which is the behaviour worth keeping.
	for topology, factory := range map[string]string{"x86": "amd64", "arm": "arm64"} {
		got, err := factoryArchitecture(topology)
		require.NoError(t, err, topology)
		assert.Equal(t, factory, got, topology)
	}

	for _, bad := range []string{"", "amd64", "arm64", "x86_64", "ARM"} {
		_, err := factoryArchitecture(bad)
		require.Error(t, err, bad)
		assert.Contains(t, err.Error(), "must be x86 or arm", bad)
	}
}

func TestImageURL_IsWhatTheFactoryServes(t *testing.T) {
	t.Parallel()

	// Pinned because a wrong path here does not fail here: it fails inside
	// hcloud-upload-image, minutes later, as a download error against a URL
	// nobody printed.
	assert.Equal(t,
		"https://factory.talos.dev/image/abc123/v1.13.10/hcloud-amd64.raw.xz",
		imageURL("abc123", "v1.13.10", "amd64"))
}

func TestRun_RejectsTheWrongNumberOfArguments(t *testing.T) {
	t.Parallel()

	// Guarded because the two arguments are a file path and a stack name, and
	// swapping them would read a topology called "dev".
	for name, args := range map[string][]string{
		"none":     {"image"},
		"one":      {"image", "topology.yaml"},
		"too many": {"image", "topology.yaml", "dev", "extra"},
	} {
		t.Run(name, func(t *testing.T) {
			// run reads os.Args, so it is set for the duration of the case.
			previous := osArgs
			osArgs = args

			defer func() { osArgs = previous }()

			err := run(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), "usage:")
		})
	}
}
