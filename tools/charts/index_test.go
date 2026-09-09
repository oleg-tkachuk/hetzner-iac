package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want Version
	}{
		{"1.2.3", Version{Major: 1, Minor: 2, Patch: 3}},
		{"v1.2.3", Version{Major: 1, Minor: 2, Patch: 3}},
		{"90.0.0", Version{Major: 90}},
		{"2.0.0-rc.1", Version{Major: 2, PreRelease: "rc.1"}},
		{"1.2.3+build.5", Version{Major: 1, Minor: 2, Patch: 3}},
		{"  1.2.3  ", Version{Major: 1, Minor: 2, Patch: 3}},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()

			got, err := ParseVersion(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestParseVersion_Rejects(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"", "1.2", "1", "latest", "1.2.x", "a.b.c", "1.2.3.4", "-1.2.3"} {
		t.Run(in, func(t *testing.T) {
			t.Parallel()

			_, err := ParseVersion(in)
			assert.Error(t, err)
		})
	}
}

func TestVersion_Compare(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		a, b     string
		expected int
	}{
		{"equal", "1.2.3", "1.2.3", 0},
		{"patch", "1.2.3", "1.2.4", -1},
		{"minor", "1.3.0", "1.2.9", 1},
		{"major", "2.0.0", "10.0.0", -1},
		// String comparison would put 10.8.4 before 9.0.0, which is exactly the
		// mistake this type exists to prevent — Argo CD's chart is on 10.x.
		{"double-digit major", "10.8.4", "9.0.0", 1},
		{"v prefix is irrelevant", "v1.21.1", "1.21.1", 0},
		// A prerelease is OLDER than the release it precedes. Getting this
		// backwards reports a stable pin as outdated against a candidate that
		// has not shipped.
		{"release beats its own prerelease", "2.0.0", "2.0.0-rc.1", 1},
		{"prerelease before release", "2.0.0-rc.1", "2.0.0", -1},
		{"prerelease ordering", "2.0.0-rc.1", "2.0.0-rc.2", -1},
		{"prerelease of a newer number still wins", "2.0.0-rc.1", "1.9.9", 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			a, err := ParseVersion(tc.a)
			require.NoError(t, err)

			b, err := ParseVersion(tc.b)
			require.NoError(t, err)

			assert.Equal(t, tc.expected, a.Compare(b))
			assert.Equal(t, -tc.expected, b.Compare(a), "comparison must be antisymmetric")
		})
	}
}

func TestVersion_IsPreRelease(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]bool{
		"1.2.3":       false,
		"1.2.3+meta":  false,
		"2.0.0-rc.1":  true,
		"2.0.0-beta":  true,
		"2.0.0-alpha": true,
	} {
		version, err := ParseVersion(in)
		require.NoError(t, err)
		assert.Equal(t, want, version.IsPreRelease(), in)
	}
}

func TestLatestStable_SkipsPreReleases(t *testing.T) {
	t.Parallel()

	// The version policy is latest STABLE, and a repository index lists
	// prereleases next to releases.
	latest, found := LatestStable([]string{"1.0.0", "2.0.0-rc.1", "1.5.0"})

	require.True(t, found)
	assert.Equal(t, "1.5.0", latest.String())
}

func TestLatestStable_SortsNumerically(t *testing.T) {
	t.Parallel()

	latest, found := LatestStable([]string{"9.0.0", "10.8.4", "10.10.0", "10.9.0"})

	require.True(t, found)
	assert.Equal(t, "10.10.0", latest.String())
}

func TestLatestStable_IgnoresUnparseableEntries(t *testing.T) {
	t.Parallel()

	// Repository indexes do contain entries that are not semver at all;
	// skipping them is right, because they cannot be the latest stable.
	latest, found := LatestStable([]string{"not-a-version", "1.2.3", "nightly"})

	require.True(t, found)
	assert.Equal(t, "1.2.3", latest.String())
}

func TestLatestStable_NoStableVersions(t *testing.T) {
	t.Parallel()

	// A real state for a young chart, and not something to fail on.
	_, found := LatestStable([]string{"1.0.0-rc.1", "1.0.0-rc.2"})
	assert.False(t, found)

	_, found = LatestStable(nil)
	assert.False(t, found)
}

func TestVersion_String_RoundTrips(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"1.2.3", "90.0.0", "2.0.0-rc.1"} {
		version, err := ParseVersion(in)
		require.NoError(t, err)
		assert.Equal(t, in, version.String())
	}
}
