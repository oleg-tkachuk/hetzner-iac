package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/env"
	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const cloud = "https://api.pulumi.com"

// project writes a Pulumi.yaml with the given backend, none when empty.
func project(t *testing.T, backend string) string {
	t.Helper()

	dir := t.TempDir()
	body := "name: fixture\nruntime: go\n"

	if backend != "" {
		body += "backend:\n  url: " + backend + "\n"
	}

	require.NoError(t, os.WriteFile(filepath.Join(dir, projectFile), []byte(body), 0o600))

	return dir
}

// credentials points the SDK at a credentials store holding these backends,
// and clears an exported token so the store is what decides.
func credentials(t *testing.T, backends ...string) {
	t.Helper()

	dir := t.TempDir()
	accounts := map[string]workspace.Account{}

	for _, backend := range backends {
		accounts[backend] = workspace.Account{AccessToken: "pul-fixture", Username: "fixture"}
	}

	raw, err := json.Marshal(workspace.Credentials{Accounts: accounts})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "credentials.json"), raw, 0o600))

	t.Setenv(workspace.PulumiCredentialsPathEnvVar, dir)
	t.Setenv(env.AccessToken.Var().Name(), "")
}

func TestRequireLogin_PassesALoggedInBackend(t *testing.T) {
	credentials(t, cloud)

	require.NoError(t, requireLogin([]string{project(t, cloud), project(t, cloud)}))
}

func TestRequireLogin_RefusesABackendWithNoCredential(t *testing.T) {
	credentials(t, "https://api.example.com")

	err := requireLogin([]string{project(t, cloud)})

	require.ErrorIs(t, err, errNotLoggedIn)
	assert.Contains(t, err.Error(), "pulumi login "+cloud)
	assert.Contains(t, err.Error(), env.AccessToken.Var().Name())
}

func TestRequireLogin_AcceptsAnExportedToken(t *testing.T) {
	credentials(t)
	t.Setenv(env.AccessToken.Var().Name(), "pul-exported")

	require.NoError(t, requireLogin([]string{project(t, cloud)}))
}

func TestRequireLogin_SkipsWhatNeedsNoAccount(t *testing.T) {
	credentials(t)

	require.NoError(t, requireLogin([]string{project(t, "file://~"), project(t, "")}),
		"a DIY backend and a project naming none have no account to check")
}

func TestRequireLogin_ReportsAMissingProject(t *testing.T) {
	credentials(t, cloud)

	err := requireLogin([]string{t.TempDir()})

	require.Error(t, err)
	assert.Contains(t, err.Error(), projectFile)
}

func TestNeedsLogin(t *testing.T) {
	t.Parallel()

	for backend, want := range map[string]bool{
		cloud:                         true,
		"http://pulumi.internal:8080": true,
		"file://~":                    false,
		"s3://bucket/state":           false,
		"gs://bucket":                 false,
		"":                            false,
		"://broken":                   false,
	} {
		assert.Equal(t, want, needsLogin(backend), backend)
	}
}
