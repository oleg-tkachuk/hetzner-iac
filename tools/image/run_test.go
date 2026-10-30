package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
)

var errBoom = errors.New("boom")

const (
	schematicHash = "abc123"
	talosVersion  = "v1.13.10"
	testToken     = "token"
)

// bakery is what run sees through deps, and what it was asked to do.
type bakery struct {
	arch     string
	images   fakeImages
	factory  fakeFactory
	failing  string
	uploaded []string
	out      bytes.Buffer
}

// The dependencies run calls, named for failing one.
const (
	stepTopology = "topology"
	stepToken    = "token"
	stepFactory  = "factory"
	stepUpload   = "upload"
)

func (b *bakery) fail(step string) error {
	if b.failing == step {
		return errBoom
	}

	return nil
}

func (b *bakery) deps() deps {
	return deps{
		topology: func(string) (*clusterspec.Topology, error) {
			return &clusterspec.Topology{
				Talos:     clusterspec.TalosSpec{Version: talosVersion, Architecture: b.arch},
				Placement: clusterspec.PlacementSpec{Location: "hel1"},
			}, b.fail(stepTopology)
		},
		token: func(context.Context, string) (string, error) { return testToken, b.fail(stepToken) },
		images: func(token string) imageLister {
			if token != testToken {
				return &fakeImages{err: errBoom}
			}

			return &b.images
		},
		factory: func() (schematicCreator, error) { return &b.factory, b.fail(stepFactory) },
		upload: func(_ context.Context, token, url, arch, location, selector string) error {
			b.uploaded = []string{token, url, arch, location, selector}

			return b.fail(stepUpload)
		},
		out: &b.out,
	}
}

func newBakery() *bakery {
	return &bakery{arch: clusterspec.ArchitectureARM, factory: fakeFactory{id: schematicHash}}
}

func TestRun_BakesTheTopologysImage(t *testing.T) {
	t.Parallel()

	b := newBakery()

	require.NoError(t, run(context.Background(), []string{"cluster.dev.yaml", "dev"}, b.deps()))

	url := "https://factory.talos.dev/image/abc123/v1.13.10/hcloud-arm64.raw.xz"
	assert.Equal(t, []string{testToken, url, "arm", "hel1", clusterspec.TalosImageSelector(talosVersion)}, b.uploaded)
	assert.Equal(t, "baking v1.13.10 (arm) in hel1 from "+url+"\n"+
		"snapshot ready — next: task cluster:apply stack=dev\n", b.out.String())
}

func TestRun_LeavesAPresentSnapshotAlone(t *testing.T) {
	t.Parallel()

	b := newBakery()
	b.images.found = []*hcloud.Image{{ID: 430516130}}

	require.NoError(t, run(context.Background(), []string{"cluster.dev.yaml", "dev"}, b.deps()))

	assert.Nil(t, b.uploaded, "nothing is baked twice")
	assert.Equal(t, "snapshot already present for "+clusterspec.TalosImageSelector(talosVersion)+
		" (arm) — nothing to do\n", b.out.String())
}

func TestRun_StopsAtTheFirstFailure(t *testing.T) {
	t.Parallel()

	for _, step := range []string{stepTopology, stepToken, stepFactory, stepUpload} {
		b := newBakery()
		b.failing = step

		require.ErrorIs(t, run(context.Background(), []string{"cluster.dev.yaml", "dev"}, b.deps()), errBoom, step)
		assert.NotContains(t, b.out.String(), "snapshot ready", step)
	}

	for name, b := range map[string]*bakery{
		"architecture": {arch: "sparc"},
		"listing":      {arch: clusterspec.ArchitectureX86, images: fakeImages{err: errBoom}},
		"schematic":    {arch: clusterspec.ArchitectureX86, factory: fakeFactory{err: errBoom}},
	} {
		require.Error(t, run(context.Background(), []string{"cluster.dev.yaml", "dev"}, b.deps()), name)
		assert.Nil(t, b.uploaded, name)
		assert.Empty(t, b.out.String(), name)
	}
}

func TestRun_PrintsTheInstallerImage(t *testing.T) {
	t.Parallel()

	b := newBakery()

	require.NoError(t, run(context.Background(), []string{installerCommand, "cluster.dev.yaml"}, b.deps()))
	assert.Equal(t, "factory.talos.dev/hcloud-installer/abc123:v1.13.10\n", b.out.String())
	assert.Nil(t, b.uploaded)

	for _, step := range []string{stepTopology, stepFactory} {
		failing := newBakery()
		failing.failing = step

		require.ErrorIs(t, run(context.Background(), []string{installerCommand, "cluster.dev.yaml"}, failing.deps()), errBoom)
		assert.Empty(t, failing.out.String(), step)
	}

	unregistered := newBakery()
	unregistered.factory.err = errBoom
	require.ErrorIs(t, run(context.Background(), []string{installerCommand, "cluster.dev.yaml"}, unregistered.deps()), errBoom)
	assert.Empty(t, unregistered.out.String())
}
