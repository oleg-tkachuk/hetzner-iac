package main

import (
	"context"
	"errors"
	"maps"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/siderolabs/image-factory/pkg/schematic"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterref"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"

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
		assert.Contains(t, err.Error(), "must be one of "+strings.Join(slices.Sorted(maps.Keys(architectures)), ", "), bad)
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
		"--location", clusterref.ProbeLocation,
		"--labels", "os=talos,talos-version=v1.13.10",
	}, uploadArgs(
		"https://factory.talos.dev/image/abc123/v1.13.10/hcloud-arm64.raw.xz",
		"arm", clusterref.ProbeLocation, "os=talos,talos-version=v1.13.10"))
}

func TestUploadArgs_LabelsWithTheSelectorTheLookupUses(t *testing.T) {
	t.Parallel()

	// The labels written here are what lookupTalosImage selects on. Stated as
	// its own case because the two programs never call each other: a snapshot
	// labelled differently is invisible to the cluster that needs it, and the
	// failure is "no available Talos snapshot" against an image that exists.
	args := uploadArgs("https://example.test/i.raw.xz", "arm", clusterref.ProbeLocation,
		clusterspec.TalosImageSelector("v1.13.10"))

	position := slices.Index(args, "--labels")
	require.NotEqual(t, -1, position)
	require.Less(t, position+1, len(args))
	assert.Equal(t, clusterspec.TalosImageSelector("v1.13.10"), args[position+1])
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

// TestArchitectures_CoverEveryOneTheTopologyAccepts holds two sets that had
// nothing comparing them.
//
// The topology validator accepts whatever is in hetzner.Architectures. This
// map decides what the Image Factory is asked for. An architecture added to
// the first and not the second passes validation, reaches the bake, and is
// refused there — after the operator has set up a token and waited.
func TestArchitectures_CoverEveryOneTheTopologyAccepts(t *testing.T) {
	t.Parallel()

	require.NotEmpty(t, clusterspec.Architectures)

	for _, arch := range clusterspec.Architectures {
		factory, err := factoryArchitecture(arch)

		require.NoError(t, err,
			"the topology accepts %q and this tool cannot bake it", arch)
		assert.NotEmpty(t, factory, arch)
	}

	// And the other way: a mapping for something the topology would reject is
	// a bake nobody can ask for.
	for arch := range architectures {
		assert.Contains(t, clusterspec.Architectures, arch,
			"%q maps to a factory architecture and the topology validator rejects it", arch)
	}
}

func TestInstallerImage_IsTheFactoryHcloudInstaller(t *testing.T) {
	t.Parallel()

	// ghcr.io/siderolabs/installer stops at v1.13; a v1.14.2 upgrade from it
	// fails on the node with "not found" before anything is touched.
	assert.Equal(t,
		"factory.talos.dev/hcloud-installer/376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba:v1.14.2",
		installerImage("376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba", "v1.14.2"))
}

func TestFactoryRegistry_IsTheFactorysHost(t *testing.T) {
	t.Parallel()

	parsed, err := url.Parse(factoryURL)
	require.NoError(t, err)
	assert.Equal(t, parsed.Host, factoryRegistry, "images and installers come from one factory")
}

// fakeImages answers a snapshot list the way hcloud-go would, and records the
// request it was given.
type fakeImages struct {
	found []*hcloud.Image
	err   error
	asked hcloud.ImageListOpts
}

func (f *fakeImages) AllWithOpts(_ context.Context, opts hcloud.ImageListOpts) ([]*hcloud.Image, error) {
	f.asked = opts

	return f.found, f.err
}

// The bug this pins: the labels carry the Talos version but not the
// architecture, so without it the check matched a snapshot of either. An Arm
// topology found the x86 one, image:bake reported "already present" and
// exited 0, and apply then failed telling the operator to run image:bake.
func TestSnapshotQuery_ScopesTheCheckToTheArchitecture(t *testing.T) {
	t.Parallel()

	for _, arch := range []hcloud.Architecture{hcloud.ArchitectureX86, hcloud.ArchitectureARM} {
		query := snapshotQuery("os=talos,talos-version=v1.13.10", string(arch))

		assert.Equal(t, []hcloud.Architecture{arch}, query.Architecture)
		assert.Equal(t, []hcloud.ImageType{hcloud.ImageTypeSnapshot}, query.Type)
		assert.Equal(t, "os=talos,talos-version=v1.13.10", query.LabelSelector)
	}
}

func TestSnapshotExists(t *testing.T) {
	t.Parallel()

	present := &fakeImages{found: []*hcloud.Image{{ID: 430516130}}}
	exists, err := snapshotExists(context.Background(), present, "os=talos", "arm")
	require.NoError(t, err)
	assert.True(t, exists)
	assert.Equal(t, []hcloud.Architecture{hcloud.ArchitectureARM}, present.asked.Architecture,
		"the architecture reaches the API")

	exists, err = snapshotExists(context.Background(), &fakeImages{}, "os=talos", "arm")
	require.NoError(t, err)
	assert.False(t, exists)

	// A failed list is an error, not "absent": reported as absent, it would
	// bake an image that already exists and leave two candidates for the
	// Pulumi lookup's mostRecent to choose between.
	_, err = snapshotExists(context.Background(), &fakeImages{err: errors.New("unauthorized")}, "os=talos", "arm")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unauthorized")
}

// fakeFactory answers SchematicCreate the way the client would.
type fakeFactory struct {
	id    string
	err   error
	asked *schematic.Schematic
}

func (f *fakeFactory) SchematicCreate(_ context.Context, sc schematic.Schematic) (string, *schematic.Schematic, error) {
	f.asked = &sc

	return f.id, &sc, f.err
}

func TestSchematicID(t *testing.T) {
	t.Parallel()

	factory := &fakeFactory{id: "376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba"}

	id, err := schematicID(context.Background(), factory)
	require.NoError(t, err)
	assert.Equal(t, factory.id, id)
	require.NotNil(t, factory.asked)
	assert.Empty(t, factory.asked.Customization.SystemExtensions.OfficialExtensions,
		"the stock image: no customisation is asked for")

	_, err = schematicID(context.Background(), &fakeFactory{err: errors.New("503")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "image factory")

	_, err = schematicID(context.Background(), &fakeFactory{})
	require.Error(t, err, "an empty id would build a URL with no schematic in it")
}
