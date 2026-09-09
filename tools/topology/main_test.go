package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validTopology = `
apiVersion: hetzner-iac/v1
kind: Cluster
metadata:
  name: platform-test
placement:
  location: hel1
network:
  adminCIDRs: [203.0.113.4/32]
talos:
  version: v1.14.0
controlPlane:
  count: 1
  serverType: cx22
`

func TestIsTopologyFile(t *testing.T) {
	t.Parallel()

	// The shape matters: infra/cluster/main.go derives the path from the stack
	// name, so a file that does not follow it is never loaded by anything and
	// sits in the repository looking like configuration that is in effect.
	for name, want := range map[string]bool{
		"cluster.prod.yaml":     true,
		"cluster.staging.yaml":  true,
		"cluster.yaml":          false, // no stack name
		"cluster.prod.yml":      false, // wrong extension
		"cluster.prod.old.yaml": false, // not a stack name main.go would build
		"Pulumi.yaml":           false,
		"prod.yaml":             false,
		"":                      false,
	} {
		assert.Equal(t, want, IsTopologyFile(name), name)
	}
}

func TestValidate_AcceptsAValidTopology(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cluster.prod.yaml"), []byte(validTopology), 0o600))

	failures, err := Validate([]string{dir})
	require.NoError(t, err)
	assert.Empty(t, failures)
}

func TestValidate_ReportsEveryInvalidFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(dir, "cluster.good.yaml"), []byte(validTopology), 0o600))
	// Two separate problems in two files: the run must report both rather than
	// stopping at the first, or fixing a topology becomes one round trip per
	// mistake.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cluster.missing.yaml"),
		[]byte("apiVersion: hetzner-iac/v1\nkind: Cluster\nmetadata: {name: x}\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cluster.malformed.yaml"),
		[]byte("this is not: [valid: yaml\n"), 0o600))

	failures, err := Validate([]string{dir})
	require.NoError(t, err)
	require.Len(t, failures, 2)

	joined := strings.Join(failures, "\n")
	assert.Contains(t, joined, "cluster.missing.yaml")
	assert.Contains(t, joined, "cluster.malformed.yaml")
	assert.NotContains(t, joined, "cluster.good.yaml")
}

func TestValidate_CatchesAnEmptyAdminCIDRList(t *testing.T) {
	t.Parallel()

	// The check that matters most: a cluster whose Kubernetes and Talos APIs
	// are open to the internet must not reach an apply.
	dir := t.TempDir()

	open := `
apiVersion: hetzner-iac/v1
kind: Cluster
metadata: {name: platform-test}
placement: {location: hel1}
network: {adminCIDRs: []}
talos: {version: v1.14.0}
controlPlane: {count: 1, serverType: cx22}
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cluster.prod.yaml"), []byte(open), 0o600))

	failures, err := Validate([]string{dir})
	require.NoError(t, err)
	require.Len(t, failures, 1)
	assert.Contains(t, failures[0], "open to the internet")
}

func TestValidate_FailsWhenThereIsNothingToCheck(t *testing.T) {
	t.Parallel()

	// A validator that reports green because it found no files is worse than
	// none: it reports success while checking nothing.
	_, err := Validate([]string{t.TempDir()})

	require.ErrorIs(t, err, ErrNoTopologies)
}

func TestValidate_FailsOnAMissingDirectory(t *testing.T) {
	t.Parallel()

	_, err := Validate([]string{filepath.Join(t.TempDir(), "absent")})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")
}

func TestValidate_IgnoresFilesThatAreNotTopologies(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cluster.prod.yaml"), []byte(validTopology), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Pulumi.yaml"), []byte("name: broken\n{{{"), 0o600))

	failures, err := Validate([]string{dir})
	require.NoError(t, err)
	assert.Empty(t, failures)
}

func TestValidate_ChecksTheRepositoryTopologies(t *testing.T) {
	t.Parallel()

	// The committed topologies must always be valid — this is the same check
	// CI runs, kept as a test so `go test ./...` catches it too.
	failures, err := Validate([]string{filepath.Join("..", "..", "infra", "cluster")})
	require.NoError(t, err)
	assert.Empty(t, failures)
}

func TestTalosVersion(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "cluster.prod.yaml")
	require.NoError(t, os.WriteFile(path, []byte(validTopology), 0o600))

	version, err := talosVersion(path)
	require.NoError(t, err)
	assert.Equal(t, "v1.14.0", version)
}

func TestTalosVersion_ReadsThroughTheRealParser(t *testing.T) {
	t.Parallel()

	// The point of this existing at all: CI installs a matching talosctl from
	// it, and the first version used `grep -A6`, which returned nothing once
	// the comment above the field grew. A parser does not care how much prose
	// sits above the value.
	dir := t.TempDir()
	path := filepath.Join(dir, "cluster.prod.yaml")

	commented := "talos:\n" + strings.Repeat("  # a long explanation\n", 20) + "  version: v1.13.10\n  architecture: x86\n"
	body := strings.Replace(validTopology, "talos:\n  version: v1.14.0\n", commented, 1)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	version, err := talosVersion(path)
	require.NoError(t, err)
	assert.Equal(t, "v1.13.10", version)
}

func TestTalosVersion_InvalidTopology(t *testing.T) {
	t.Parallel()

	// A malformed file must fail loudly rather than print an empty string that
	// a shell would splice into a download URL.
	dir := t.TempDir()
	path := filepath.Join(dir, "cluster.prod.yaml")
	require.NoError(t, os.WriteFile(path, []byte("not: a topology\n"), 0o600))

	_, err := talosVersion(path)
	require.Error(t, err)
}

func TestTalosVersion_MatchesTheRepositoryTopologies(t *testing.T) {
	t.Parallel()

	// What CI actually runs.
	version, err := talosVersion(filepath.Join("..", "..", "infra", "cluster", "cluster.prod.yaml"))
	require.NoError(t, err)
	assert.Regexp(t, `^v\d+\.\d+\.\d+$`, version)
}
