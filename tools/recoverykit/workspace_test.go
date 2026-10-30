package main

import (
	"errors"
	"fmt"
	"os/exec"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOutputsJSON_IsWhatTheCLIPrinted pins the backup-outputs part to the
// document `pulumi stack output --json --show-secrets` printed: keys sorted,
// two-space indent, a trailing newline, and no HTML escaping of a password.
func TestOutputsJSON_IsWhatTheCLIPrinted(t *testing.T) {
	t.Parallel()

	out, err := outputsJSON(auto.OutputMap{
		"resticPassword": {Value: "p<a>s&s", Secret: true},
		"backupHost":     {Value: "u1.your-storagebox.de"},
		"backupPort":     {Value: float64(23)},
		"paths":          {Value: []any{"etcd", "volumes"}},
	})
	require.NoError(t, err)

	assert.Equal(t, `{
  "backupHost": "u1.your-storagebox.de",
  "backupPort": 23,
  "paths": [
    "etcd",
    "volumes"
  ],
  "resticPassword": "p<a>s&s"
}
`, string(out))
}

// TestOutputsJSON_UnwrapsSecrets is the reason the kit exists: a secret output
// is its plaintext, with no trace of the flag the automation API kept.
func TestOutputsJSON_UnwrapsSecrets(t *testing.T) {
	t.Parallel()

	out, err := outputsJSON(auto.OutputMap{"resticPassword": {Value: "hunter2", Secret: true}})
	require.NoError(t, err)

	assert.JSONEq(t, `{"resticPassword": "hunter2"}`, string(out))
	assert.NotContains(t, string(out), "Secret")
	assert.NotContains(t, string(out), "Value")
}

// TestOutputsJSON_NoOutputsIsAnEmptyObject is what backupOutputs reads as a
// tier that was never applied.
func TestOutputsJSON_NoOutputsIsAnEmptyObject(t *testing.T) {
	t.Parallel()

	for name, outputs := range map[string]auto.OutputMap{"nil": nil, "empty": {}} {
		out, err := outputsJSON(outputs)
		require.NoError(t, err, name)
		assert.Equal(t, "{}\n", string(out), name)
	}
}

func TestOutputsJSON_RefusesAValueJSONCannotHold(t *testing.T) {
	t.Parallel()

	_, err := outputsJSON(auto.OutputMap{"broken": {Value: func() {}}})
	require.Error(t, err)
}

// TestStackNames_KeepsTheBackendsSpelling leaves an organisation-qualified
// name as listed: matching `acme/dev` to `dev` is stackstatus.SameStack's job,
// and backupOutputs relies on it.
func TestStackNames_KeepsTheBackendsSpelling(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"dev", "acme/prod"},
		stackNames([]auto.StackSummary{{Name: "dev", Current: true}, {Name: "acme/prod"}}))
	assert.Empty(t, stackNames(nil))
}

// captured stands for the automation API's error: its text carries the
// command's stdout, and Unwrap reaches the SDK's own message.
type captured struct {
	stdout string
	inner  error
}

func (c captured) Error() string { return fmt.Sprintf("%v\nstdout: %s", c.inner, c.stdout) }

func (c captured) Unwrap() error { return c.inner }

// TestWithoutCapturedOutput_KeepsDecryptedStdoutOffStderr is a read that
// succeeded and could not be decoded: its stdout is the plaintext, and the
// error printed to stderr must not carry it.
func TestWithoutCapturedOutput_KeepsDecryptedStdoutOffStderr(t *testing.T) {
	t.Parallel()

	decode := errors.New("unable to unmarshall stack state")

	err := withoutCapturedOutput(captured{stdout: `{"secretbox":"plaintext-key"}`, inner: decode})

	require.ErrorIs(t, err, decode)
	assert.NotContains(t, err.Error(), "plaintext-key")
}

// TestWithoutCapturedOutput_KeepsAFailedCommandsDiagnosis is the ordinary
// failure — an expired login, a missing stack — whose stderr is what the
// operator needs, and whose stdout is empty.
func TestWithoutCapturedOutput_KeepsAFailedCommandsDiagnosis(t *testing.T) {
	t.Parallel()

	failed := captured{inner: fmt.Errorf("could not export stack: %w", &exec.ExitError{})}

	assert.Equal(t, error(failed), withoutCapturedOutput(failed))
}

func TestWithoutCapturedOutput_LeavesAPlainErrorAlone(t *testing.T) {
	t.Parallel()

	plain := errors.New("context deadline exceeded")

	assert.Equal(t, plain, withoutCapturedOutput(plain))
}
