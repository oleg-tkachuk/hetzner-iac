package imagepolicy_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/imagepolicy"
)

// clusterImagePolicy is a rendered policy, every field the template writes —
// read strictly, so a key the template misspells fails here.
type clusterImagePolicy struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Mode   string `json:"mode"`
		Images []struct {
			Glob string `json:"glob"`
		} `json:"images"`
		Authorities []struct {
			Name            string `json:"name"`
			SignatureFormat string `json:"signatureFormat"`
			Static          *struct {
				Action string `json:"action"`
			} `json:"static"`
			Key *struct {
				Data          string `json:"data"`
				HashAlgorithm string `json:"hashAlgorithm"`
			} `json:"key"`
			Keyless *struct {
				Identities []struct {
					Issuer        string `json:"issuer"`
					Subject       string `json:"subject"`
					SubjectRegExp string `json:"subjectRegExp"`
				} `json:"identities"`
			} `json:"keyless"`
			CTLog *struct {
				URL string `json:"url"`
			} `json:"ctlog"`
		} `json:"authorities"`
	} `json:"spec"`
}

func render(t *testing.T, doc string, mode imagepolicy.Mode) map[string]clusterImagePolicy {
	t.Helper()

	inventory, err := imagepolicy.Parse([]byte(doc))
	require.NoError(t, err)

	stream, err := inventory.Policies(mode)
	require.NoError(t, err)

	out := map[string]clusterImagePolicy{}

	for _, part := range strings.Split(stream, "\n---\n") {
		if strings.TrimSpace(strings.TrimPrefix(part, "---")) == "" {
			continue
		}

		var p clusterImagePolicy
		require.NoError(t, yaml.UnmarshalStrict([]byte(part), &p), part)
		out[p.Metadata.Name] = p
	}

	return out
}

func globs(p clusterImagePolicy) []string {
	var out []string
	for _, image := range p.Spec.Images {
		out = append(out, image.Glob)
	}

	return out
}

func TestPolicies_PinAnUnsignedImageToItsDigestAlone(t *testing.T) {
	t.Parallel()

	policies := render(t, "images: [{repository: docker.io/library/a, unsigned: "+pin+"}]", imagepolicy.ModeWarn)

	p, found := policies[imagepolicy.PolicyName("docker.io/library/a")]
	require.True(t, found)
	assert.Equal(t, string(imagepolicy.ModeWarn), p.Spec.Mode)
	// index.docker.io: the spelling the webhook matches against, and the
	// one place Docker Hub is not docker.io.
	assert.Equal(t, []string{"index.docker.io/library/a@sha256:" + sixtyFourHex()}, globs(p),
		"only the pinned digest, never a tag that can move")
	require.Len(t, p.Spec.Authorities, 1)
	require.NotNil(t, p.Spec.Authorities[0].Static)
	assert.Equal(t, "pass", p.Spec.Authorities[0].Static.Action)
}

func TestPolicies_HoldAKeylessImageToItsIdentity(t *testing.T) {
	t.Parallel()

	policies := render(t, `images:
  - repository: quay.io/cilium/cilium
    signed: {issuer: https://token.actions.githubusercontent.com, subjectRegExp: '^https://github\.com/cilium/.+$', format: bundle}
  - repository: ghcr.io/a/b
    signed: {issuer: https://token.actions.githubusercontent.com, subject: https://github.com/a/b/w.yml@refs/heads/main}
`, imagepolicy.ModeEnforce)

	cilium := policies[imagepolicy.PolicyName("quay.io/cilium/cilium")]
	assert.Equal(t, string(imagepolicy.ModeEnforce), cilium.Spec.Mode)
	assert.Equal(t, []string{"quay.io/cilium/cilium@*", "quay.io/cilium/cilium:*"}, globs(cilium))

	authority := cilium.Spec.Authorities[0]
	assert.Equal(t, imagepolicy.FormatBundle, authority.SignatureFormat)
	require.NotNil(t, authority.Keyless)
	assert.Equal(t, `^https://github\.com/cilium/.+$`, authority.Keyless.Identities[0].SubjectRegExp,
		"the backslashes survive the quoting")
	assert.Empty(t, authority.Keyless.Identities[0].Subject)

	exact := policies[imagepolicy.PolicyName("ghcr.io/a/b")].Spec.Authorities[0]
	assert.Empty(t, exact.SignatureFormat, "cosign's legacy format is the default")
	assert.Equal(t, "https://github.com/a/b/w.yml@refs/heads/main", exact.Keyless.Identities[0].Subject)
}

func TestPolicies_HoldAKeySignedImageToTheCommittedKey(t *testing.T) {
	t.Parallel()

	policies := render(t, `images:
  - repository: quay.io/jetstack/a
    signed: {key: cert-manager.pem, hashAlgorithm: sha512, noTransparencyLog: true}
  - repository: quay.io/jetstack/b
    signed: {key: cert-manager.pem}
`, imagepolicy.ModeWarn)

	key, err := imagepolicy.PublicKey("cert-manager.pem")
	require.NoError(t, err)

	noLog := policies[imagepolicy.PolicyName("quay.io/jetstack/a")].Spec.Authorities[0]
	require.NotNil(t, noLog.Key)
	assert.Equal(t, strings.TrimSpace(string(key)), strings.TrimSpace(noLog.Key.Data))
	assert.Equal(t, "sha512", noLog.Key.HashAlgorithm)
	assert.Nil(t, noLog.CTLog, "a publisher that records nothing in Rekor must not be asked for an entry")

	logged := policies[imagepolicy.PolicyName("quay.io/jetstack/b")].Spec.Authorities[0]
	assert.Equal(t, imagepolicy.DefaultHashAlgorithm, logged.Key.HashAlgorithm)
	require.NotNil(t, logged.CTLog)
	assert.Equal(t, imagepolicy.PublicRekor, logged.CTLog.URL)
}

func TestPolicies_CoverTheWholeInventory(t *testing.T) {
	t.Parallel()

	inventory, err := imagepolicy.Load()
	require.NoError(t, err)

	stream, err := inventory.Policies(imagepolicy.ModeWarn)
	require.NoError(t, err)

	for _, repository := range inventory.Repositories() {
		assert.Contains(t, stream, "name: "+imagepolicy.PolicyName(repository)+"\n")
	}
}

func TestPolicies_RefuseAModeTheyDoNotHave(t *testing.T) {
	t.Parallel()

	inventory, err := imagepolicy.Load()
	require.NoError(t, err)

	_, err = inventory.Policies("audit")
	require.Error(t, err)
}

func TestNoMatchPolicy_FollowsTheMode(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "warn", imagepolicy.ModeWarn.NoMatchPolicy())
	assert.Equal(t, "deny", imagepolicy.ModeEnforce.NoMatchPolicy(),
		"under enforce an image outside the inventory is refused like one that fails")
}

func TestParse_RefusesABundleSignedByAKey(t *testing.T) {
	t.Parallel()

	// policy-controller verifies bundles for keyless signers only.
	_, err := imagepolicy.Parse([]byte(
		"images: [{repository: docker.io/library/a, signed: {key: cert-manager.pem, format: bundle}}]"))
	require.Error(t, err)

	_, err = imagepolicy.Parse([]byte(
		"images: [{repository: docker.io/library/a, signed: {issuer: i, subject: s, format: oci}}]"))
	require.Error(t, err, "a format policy-controller does not know")
}
