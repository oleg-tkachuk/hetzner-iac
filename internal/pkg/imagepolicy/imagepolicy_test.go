package imagepolicy_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/imagepolicy"
)

func TestLoad_TheCommittedInventoryIsValid(t *testing.T) {
	t.Parallel()

	inventory, err := imagepolicy.Load()
	require.NoError(t, err)
	assert.NotEmpty(t, inventory.Images)
}

func TestRepository_IsTheFullyQualifiedName(t *testing.T) {
	t.Parallel()

	for image, want := range map[string]string{
		"traefik:v3.7.13":                                        "docker.io/library/traefik",
		"hetznercloud/hcloud-csi-driver:v2.23.0":                 "docker.io/hetznercloud/hcloud-csi-driver",
		"quay.io/cilium/cilium:v1.20.2@sha256:" + sixtyFourHex(): "quay.io/cilium/cilium",
		"registry.k8s.io/sig-storage/livenessprobe:v2.18.0":      "registry.k8s.io/sig-storage/livenessprobe",
	} {
		got, err := imagepolicy.Repository(image)
		require.NoError(t, err, image)
		assert.Equal(t, want, got, image)
	}

	_, err := imagepolicy.Repository("library/Not_A Name")
	require.Error(t, err)
}

func sixtyFourHex() string {
	digest := ""
	for range 64 {
		digest += "a"
	}

	return digest
}

func TestLookup_IgnoresTagAndDigest(t *testing.T) {
	t.Parallel()

	inventory, err := imagepolicy.Load()
	require.NoError(t, err)

	entry, found, err := inventory.Lookup("traefik:v9.9.9")
	require.NoError(t, err)
	require.True(t, found)
	assert.NotEmpty(t, entry.Unsigned)

	_, found, err = inventory.Lookup("docker.io/library/busybox:1")
	require.NoError(t, err)
	assert.False(t, found)
}

func TestParse_RefusesWhatNoPolicyCouldBeBuiltFrom(t *testing.T) {
	t.Parallel()

	for name, doc := range map[string]string{
		"neither":             "images: [{repository: docker.io/library/a}]",
		"both":                "images: [{repository: docker.io/library/a, unsigned: x, signed: {issuer: i, subject: s}}]",
		"keyless, no issuer":  "images: [{repository: docker.io/library/a, signed: {subject: s}}]",
		"subject and pattern": "images: [{repository: docker.io/library/a, signed: {issuer: i, subject: s, subjectRegExp: r}}]",
		"neither subject":     "images: [{repository: docker.io/library/a, signed: {issuer: i}}]",
		"key and keyless":     "images: [{repository: docker.io/library/a, signed: {key: cert-manager.pem, issuer: i}}]",
		"missing key file":    "images: [{repository: docker.io/library/a, signed: {key: nobody.pem}}]",
		"broken pattern":      "images: [{repository: docker.io/library/a, signed: {issuer: i, subjectRegExp: '('}}]",
		"short form":          "images: [{repository: traefik, unsigned: x}]",
		"listed twice":        "images: [{repository: docker.io/library/a, unsigned: x}, {repository: docker.io/library/a, unsigned: y}]",
		"unknown field":       "images: [{repository: docker.io/library/a, unsigned: x, digest: y}]",
	} {
		_, err := imagepolicy.Parse([]byte(doc))
		require.Error(t, err, name)
	}

	_, err := imagepolicy.Parse([]byte(
		"images: [{repository: docker.io/library/a, signed: {key: cert-manager.pem, hashAlgorithm: sha512}}]"))
	require.NoError(t, err, "a key-signed entry with a committed key")
}
