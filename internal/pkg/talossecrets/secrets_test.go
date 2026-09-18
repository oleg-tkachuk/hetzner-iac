package talossecrets

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The shape `pulumi stack export --show-secrets` returns, trimmed to what
// bundleFrom reads. The engine keys are real: a live export carries both.
const exportedWithSecrets = `{
  "deployment": {
    "resources": [
      {"urn": "urn:pulumi:dev::hetzner-cluster::hcloud:index/network:Network::platform-dev", "type": "hcloud:index/network:Network", "outputs": {"id": "1"}},
      {"urn": "urn:pulumi:dev::hetzner-cluster::talos:machine/secrets:Secrets::platform-dev-secrets",
       "type": "talos:machine/secrets:Secrets",
       "outputs": {
         "talosVersion": "v1.13.10",
         "machineSecrets": {"cluster": {"id": "abc", "secret": {"4dabf18193072939515e22adb298388d": "1b47061264138c4ac30d75fd1eb44270", "plaintext": "\"shh\""}}},
         "clientConfiguration": {"caCertificate": "LS0t"},
         "__meta": "{\"schema_version\":\"0\"}",
         "__pulumi_raw_state_delta": {"anything": true}
       }}
    ]
  }
}`

func TestBundleFrom_ReturnsTheSecretsResourceOutputs(t *testing.T) {
	t.Parallel()

	out, err := bundleFrom([]byte(exportedWithSecrets), "dev")
	require.NoError(t, err)

	var bundle map[string]any
	require.NoError(t, json.Unmarshal(out, &bundle))

	assert.Equal(t, "v1.13.10", bundle["talosVersion"])
	assert.Contains(t, bundle, "machineSecrets", "without this the bundle unlocks nothing")
	assert.Contains(t, bundle, "clientConfiguration")
}

func TestBundleFrom_DropsTheEngineBookkeeping(t *testing.T) {
	t.Parallel()

	out, err := bundleFrom([]byte(exportedWithSecrets), "dev")
	require.NoError(t, err)

	var bundle map[string]any
	require.NoError(t, json.Unmarshal(out, &bundle))

	// They describe the state, not the cluster. Kept, they invite the person
	// reading this file under pressure to wonder whether they matter.
	for _, key := range []string{"__meta", "__pulumi_raw_state_delta"} {
		assert.NotContains(t, bundle, key)
	}
}

func TestBundleFrom_Errors(t *testing.T) {
	t.Parallel()

	const two = `{"deployment":{"resources":[
		{"type":"talos:machine/secrets:Secrets","outputs":{"talosVersion":"a"}},
		{"type":"talos:machine/secrets:Secrets","outputs":{"talosVersion":"b"}}]}}`

	for name, tc := range map[string]struct {
		raw  string
		want string
	}{
		// An empty file stored as a backup is worse than no file: it is only
		// read on the day it is needed.
		"no secrets resource": {`{"deployment":{"resources":[]}}`, "holds no talos:machine/secrets:Secrets"},
		"not the cluster tier": {
			`{"deployment":{"resources":[{"type":"kubernetes:helm.sh/v3:Release","outputs":{}}]}}`,
			"holds no talos:machine/secrets:Secrets",
		},
		// Reported as "the first one", this would store one of two
		// certificate authorities and say nothing about the choice.
		"two of them": {two, "choosing between certificate authorities"},

		"unparseable": {"not json", "no usable json"},
	} {
		_, err := bundleFrom([]byte(tc.raw), "dev")

		require.Error(t, err, name)
		assert.Contains(t, err.Error(), tc.want, name)
	}
}

func TestBundle_RefusesAnEmptyStack(t *testing.T) {
	t.Parallel()

	// Without the guard the export would run against the SELECTED stack and
	// hand back another environment's certificate authority, confidently.
	_, err := Bundle(t.Context(), "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no stack")
}

func TestBundleFrom_UnwrapsThePulumiSecretEnvelope(t *testing.T) {
	t.Parallel()

	out, err := bundleFrom([]byte(exportedWithSecrets), "dev")
	require.NoError(t, err)

	var bundle map[string]any
	require.NoError(t, json.Unmarshal(out, &bundle))

	cluster, ok := bundle["machineSecrets"].(map[string]any)["cluster"].(map[string]any)
	require.True(t, ok)

	// The value Talos wants, not the shape Pulumi stores it in. Left wrapped,
	// unwrapping falls to whoever is restoring a cluster.
	assert.Equal(t, "shh", cluster["secret"])
	assert.Equal(t, "abc", cluster["id"], "a value that was never a secret must pass through unchanged")
}

func TestUnwrapSecrets_LeavesOtherEnvelopesAlone(t *testing.T) {
	t.Parallel()

	// Pulumi signs assets and output values the same way, with a different
	// signature. Unwrapping one of those would corrupt it.
	other := map[string]any{
		secretSignatureKey: "d0e6a833031e9bbcd3f4e8bde6ca49a4", // gitleaks:allow
		"value":            "kept",
	}

	got, err := unwrapSecrets(other)

	require.NoError(t, err)
	assert.Equal(t, other, got)
}

func TestUnwrapSecrets_RefusesAnEnvelopeWithNothingInIt(t *testing.T) {
	t.Parallel()

	// Dropped instead, the bundle would be missing one certificate authority
	// and would restore a cluster that rejects every certificate in it.
	_, err := unwrapSecrets(map[string]any{
		secretSignatureKey: secretSignature,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "carries no plaintext")
}

func TestUnwrapSecrets_ReachesInsideArrays(t *testing.T) {
	t.Parallel()

	got, err := unwrapSecrets([]any{
		map[string]any{secretSignatureKey: secretSignature, plaintextKey: `"first"`},
		"second",
	})

	require.NoError(t, err)
	assert.Equal(t, []any{"first", "second"}, got)
}
