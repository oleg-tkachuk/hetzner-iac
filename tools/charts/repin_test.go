package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
