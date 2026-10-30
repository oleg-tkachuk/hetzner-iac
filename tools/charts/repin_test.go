package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/charts"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/imagepolicy"
)

const pinnedInventory = `# A comment the rewrite must keep.
images:
  - repository: docker.io/library/traefik
    unsigned:
      reason: Traefik publishes no cosign signature.
      tag: v3.7.13
      digest: sha256:` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `
  - repository: quay.io/cilium/cilium
    signed: {issuer: i, subjectRegExp: '^https://github\.com/cilium/.+$'}
`

func TestSetPin_ChangesOnlyTheTagAndDigest(t *testing.T) {
	t.Parallel()

	digest := "sha256:" + strings.Repeat("b", 64)

	out, err := setPin([]byte(pinnedInventory), "docker.io/library/traefik", "v3.8.0", digest)
	require.NoError(t, err)

	want := strings.Replace(strings.Replace(pinnedInventory,
		"tag: v3.7.13", "tag: v3.8.0", 1),
		"digest: sha256:"+strings.Repeat("a", 64), "digest: "+digest, 1)
	assert.Equal(t, want, string(out), "every other byte — comments, quoting, the pattern — is left alone")

	inventory, err := imagepolicy.Parse(out)
	require.NoError(t, err)

	pin, err := inventory.Pinned("docker.io/library/traefik")
	require.NoError(t, err)
	assert.Equal(t, "v3.8.0@"+digest, pin.Reference())
}

func TestSetPin_RefusesWhatHasNoPin(t *testing.T) {
	t.Parallel()

	_, err := setPin([]byte(pinnedInventory), "quay.io/cilium/cilium", "v1", "sha256:x")
	require.ErrorIs(t, err, errNoPin, "a signed repository has no pin to move")

	_, err = setPin([]byte(pinnedInventory), "docker.io/library/redis", "v1", "sha256:x")
	require.Error(t, err, "a repository with no entry")
}

const traefikRepository = "docker.io/library/traefik"

var newDigest = "sha256:" + strings.Repeat("b", 64)

// pinnedFile writes pinnedInventory to a temporary file and returns its path
// and the inventory it parses to.
func pinnedFile(t *testing.T) (string, *imagepolicy.Inventory) {
	t.Helper()

	path := filepath.Join(t.TempDir(), imagepolicy.File)
	require.NoError(t, os.WriteFile(path, []byte(pinnedInventory), inventoryMode))

	inventory, err := imagepolicy.Parse([]byte(pinnedInventory))
	require.NoError(t, err)

	return path, inventory
}

// traefikRenders is a render whose traefik chart runs tag on its defaults.
func traefikRenders(tag string) renderFunc {
	return renderFixture{manifests: map[string]string{
		charts.Traefik: podManifest("traefik", "docker.io/traefik:"+tag),
	}}.render
}

// resolvesTo resolves every tag to digest.
func resolvesTo(digest string) resolveFunc {
	return func(context.Context, string, string) (string, error) { return digest, nil }
}

func TestRepinFile_MovesThePinToTheRenderedTag(t *testing.T) {
	t.Parallel()

	path, inventory := pinnedFile(t)

	var out bytes.Buffer

	require.NoError(t, repinFile(t.Context(), &out, inventory, path, traefikRenders("v3.8.0"), resolvesTo(newDigest)))

	assert.Equal(t, "pinned  "+traefikRepository+"  v3.7.13@sha256:"+strings.Repeat("a", 64)+
		" → v3.8.0@"+newDigest+"\n", out.String())

	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	moved, err := setPin([]byte(pinnedInventory), traefikRepository, "v3.8.0", newDigest)
	require.NoError(t, err)
	assert.Equal(t, string(moved), string(raw))
}

func TestRepinFile_LeavesAPinThatFollowsItsChart(t *testing.T) {
	t.Parallel()

	path, inventory := pinnedFile(t)

	var out bytes.Buffer

	require.NoError(t, repinFile(t.Context(), &out, inventory, path, traefikRenders("v3.7.13"),
		resolvesTo("sha256:"+strings.Repeat("a", 64))))
	assert.Equal(t, "every pin already follows its chart\n", out.String())

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, pinnedInventory, string(raw))
}

func TestRepinFile_Errors(t *testing.T) {
	t.Parallel()

	path, inventory := pinnedFile(t)

	failing := renderFixture{failing: map[string]bool{charts.Cilium: true}}.render
	err := repinFile(t.Context(), io.Discard, inventory, path, failing, resolvesTo(newDigest))
	require.ErrorIs(t, err, errFixture)
	assert.Contains(t, err.Error(), "cilium: default render failed")

	err = repinFile(t.Context(), io.Discard, inventory, filepath.Join(t.TempDir(), "absent.yaml"),
		traefikRenders("v3.8.0"), resolvesTo(newDigest))
	require.ErrorIs(t, err, os.ErrNotExist)

	unresolved := func(context.Context, string, string) (string, error) { return "", errFixture }
	err = repinFile(t.Context(), io.Discard, inventory, path, traefikRenders("v3.8.0"), unresolved)
	require.ErrorIs(t, err, errFixture)

	raw, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, pinnedInventory, string(raw), "a failed repin writes nothing")
}

func TestMovePins_Errors(t *testing.T) {
	t.Parallel()

	_, inventory := pinnedFile(t)

	_, _, err := movePins(t.Context(), io.Discard, inventory, map[string]string{"quay.io/cilium/cilium": "v1"},
		[]byte(pinnedInventory), resolvesTo(newDigest))
	require.Error(t, err, "a signed repository has no pin to move")

	_, _, err = movePins(t.Context(), io.Discard, inventory, map[string]string{traefikRepository: "v3.8.0"},
		[]byte("images: []\n"), resolvesTo(newDigest))
	require.Error(t, err, "a text without the entry cannot be rewritten")
	assert.Contains(t, err.Error(), "has no entry")
}

func TestAddUnsignedTags(t *testing.T) {
	t.Parallel()

	inventory := testInventory(t)
	tags := map[string]string{}

	require.NoError(t, addUnsignedTags(tags, []string{
		"docker.io/traefik:v3.8.0", "quay.io/cilium/cilium:v1", "docker.io/library/busybox:1",
	}, inventory))
	assert.Equal(t, map[string]string{traefikRepository: "v3.8.0"}, tags,
		"only a pinned repository has a tag to follow")

	require.Error(t, addUnsignedTags(tags, []string{"Not A Name"}, inventory))
}

// resolveDigest against an in-process registry: the client is the real one,
// and only the server is local.
func TestResolveDigest(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(server.Close)

	repository := strings.TrimPrefix(server.URL, "http://") + "/library/traefik"

	image, err := random.Image(1, 1)
	require.NoError(t, err)

	ref, err := name.NewTag(repository + ":v3.8.0")
	require.NoError(t, err)
	require.NoError(t, remote.Write(ref, image, remote.WithContext(t.Context())))

	want, err := image.Digest()
	require.NoError(t, err)

	got, err := resolveDigest(t.Context(), repository, "v3.8.0")
	require.NoError(t, err)
	assert.Equal(t, want.String(), got)

	_, err = resolveDigest(t.Context(), repository, "v0.0.0")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolve "+repository+":v0.0.0")

	_, err = resolveDigest(t.Context(), repository, "not a tag")
	require.Error(t, err)
}
