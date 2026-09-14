package main

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/hetzner"

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

func TestUploadArgs_BakesInTheTopologysLocation(t *testing.T) {
	t.Parallel()

	// --location is the fix. hcloud-upload-image defaults to fsn1, and it
	// bakes by creating a real server, so the default decided where — and the
	// cax line is not offered everywhere. A cluster in hel1 could have its
	// image baked in a location that cannot hold the server type.
	assert.Equal(t, []string{
		"upload",
		"--image-url", "https://factory.talos.dev/image/abc123/v1.13.10/hcloud-arm64.raw.xz",
		"--compression", "xz",
		"--architecture", "arm",
		"--location", "hel1",
		"--labels", "os=talos,talos-version=v1.13.10",
	}, uploadArgs(
		"https://factory.talos.dev/image/abc123/v1.13.10/hcloud-arm64.raw.xz",
		"arm", "hel1", "os=talos,talos-version=v1.13.10"))
}

func TestUploadArgs_LabelsWithTheSelectorTheLookupUses(t *testing.T) {
	t.Parallel()

	// The labels written here are what lookupTalosImage selects on. Stated as
	// its own case because the two programs never call each other: a snapshot
	// labelled differently is invisible to the cluster that needs it, and the
	// failure is "no available Talos snapshot" against an image that exists.
	args := uploadArgs("https://example.test/i.raw.xz", "arm", "hel1",
		hetzner.TalosImageSelector("v1.13.10"))

	position := slices.Index(args, "--labels")
	require.NotEqual(t, -1, position)
	require.Less(t, position+1, len(args))
	assert.Equal(t, hetzner.TalosImageSelector("v1.13.10"), args[position+1])
}

func TestCreatedSchematic_AcceptsTheStatusTheFactoryActuallySends(t *testing.T) {
	t.Parallel()

	// 201 is the regression. Demanding exactly 200 made every bake fail with
	// "image factory returned 201 Created", invisibly, for as long as the
	// project already had a snapshot — the presence check returns before the
	// factory is called.
	for _, status := range []int{
		http.StatusOK,
		http.StatusCreated,
		http.StatusAccepted,
	} {
		assert.True(t, createdSchematic(status), status)
	}

	// Everything that is not the factory handing back a schematic: a redirect
	// to a login page, a rejected body, a broken factory. decodeSchematic
	// cannot tell these apart from a valid response, so the status must.
	for _, status := range []int{
		http.StatusMultipleChoices,
		http.StatusFound,
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
	} {
		assert.False(t, createdSchematic(status), status)
	}
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

func TestSnapshotMatched_PresentAbsentAndBroken(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		raw     string
		want    bool
		wantErr string
	}{
		"one snapshot":  {`[{"id":1,"description":"talos v1.13.10"}]`, true, ""},
		"two snapshots": {`[{"id":1},{"id":2}]`, true, ""},
		// The ordinary first-run state, and not an error.
		"none": {`[]`, false, ""},
		// The distinction awk could not make: it answered "no rows" with exit
		// 1 for both an empty list and a failed call.
		"not json":   {"talos v1.13.10", false, "no usable json"},
		"empty body": {"", false, "no usable json"},
	} {
		got, err := snapshotMatched([]byte(tc.raw))

		if tc.wantErr != "" {
			require.Error(t, err, name)
			assert.Contains(t, err.Error(), tc.wantErr, name)

			continue
		}

		require.NoError(t, err, name)
		assert.Equal(t, tc.want, got, name)
	}
}

func TestDecodeSchematic(t *testing.T) {
	t.Parallel()

	id, err := decodeSchematic(strings.NewReader(`{"id":"abc123"}`))
	require.NoError(t, err)
	assert.Equal(t, "abc123", id)
}

func TestDecodeSchematic_Errors(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		body string
		want string
	}{
		// Both would build a URL the factory serves nothing at, and that
		// failure arrives minutes later from hcloud-upload-image.
		"no id":    {`{}`, "no schematic id"},
		"empty id": {`{"id":""}`, "no schematic id"},
		"not json": {`<html>502</html>`, "image factory response"},
	} {
		_, err := decodeSchematic(strings.NewReader(tc.body))

		require.Error(t, err, name)
		assert.Contains(t, err.Error(), tc.want, name)
	}
}
