package ci

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/hetzner"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// topologyLocation matches the location a committed topology names.
var topologyLocation = regexp.MustCompile(`(?m)^\s*location:\s*([a-z0-9]+)\s*$`)

// yamlFixtureLocation matches the same line inside a Go string literal, which
// is how the parser's fixtures are written.
//
// Anchored to the end of the line, and that is not tidiness: the unanchored
// form matched the prose "The cluster's own location: the upload then stays
// inside one region" and reported a fixture in location "the". A gate whose
// first failure is its own false positive teaches everyone to skip it.
//
// The flow form `placement: {location: hel1}` is matched too, which is why the
// value may be followed by a brace.
var yamlFixtureLocation = regexp.MustCompile(`(?m)location:\s*([a-z0-9]+)\s*\}?\s*$`)

// exampleTopology is the committed cluster config — the file an operator
// copies, and the anchor for what a location is.
const exampleTopology = "infra/cluster/cluster.example.yaml"

// TestProbeLocation_IsTheOneTheClusterConfigNames is the contract, and it runs
// from internal/ci because it is the only package that can see all three
// halves: the constant, the validator's set, and the committed file.
//
// One value in one place was the point. It used to be sixteen literals, and
// the risk was never the typing — it was a fixture naming a location the
// validator and the JSON schema have never heard of, which tests a cluster
// that cannot exist and reports green.
func TestProbeLocation_IsTheOneTheClusterConfigNames(t *testing.T) {
	t.Parallel()

	require.NotEmpty(t, hetzner.Locations, "the validator knows no locations")

	assert.True(t, slices.Contains(hetzner.Locations, platform.ProbeLocation),
		"platform.ProbeLocation is %q, which hetzner.Locations does not accept — every "+
			"fixture using it builds a topology the validator refuses",
		platform.ProbeLocation)

	raw, err := os.ReadFile(filepath.Join("..", "..", exampleTopology))
	require.NoError(t, err)

	found := topologyLocation.FindStringSubmatch(string(raw))
	require.NotNil(t, found, "%s names no location", exampleTopology)

	assert.Equal(t, found[1], platform.ProbeLocation,
		"%s names %q and platform.ProbeLocation is %q; the committed cluster config is where "+
			"a location comes from, so the fixtures follow it rather than the other way round",
		exampleTopology, found[1], platform.ProbeLocation)
}

// TestYAMLFixtures_NameALocationThatExists keeps the parser's fixtures
// readable without letting them drift.
//
// They are raw YAML in Go string literals on purpose — a fixture that reads
// like the file it stands for is worth more than one assembled from
// constants. What that costs is a literal the compiler cannot check, and this
// is the check: whatever a fixture names has to be a location this platform
// builds in.
func TestYAMLFixtures_NameALocationThatExists(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	var checked int

	for _, dir := range []string{
		filepath.Join(root, "internal", "pkg", "hetzner"),
		filepath.Join(root, "tools", "topology"),
	} {
		entries, err := os.ReadDir(dir)
		require.NoError(t, err, dir)

		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, "_test.go") {
				continue
			}

			path := filepath.Join(dir, name)

			raw, readErr := os.ReadFile(path)
			require.NoError(t, readErr, path)

			for _, match := range yamlFixtureLocation.FindAllStringSubmatch(string(raw), -1) {
				location := match[1]

				checked++

				assert.True(t, slices.Contains(hetzner.Locations, location),
					"%s has a fixture in location %q, which is not one this platform builds in",
					relativeToRoot(path), location)
			}
		}
	}

	assert.Positive(t, checked, "no YAML fixture names a location; this test is checking nothing")
}
