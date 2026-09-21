package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests here pin what this tool RELIES ON from Masterminds/semver, not the
// library's own correctness, which is its business. The distinction matters
// because the parsing used to be written out in index.go: these cases are the
// contract that survived the move, and a library upgrade that broke one of
// them would change which chart is reported as outdated.

func TestParseVersion_TakesTheSpellingsAnIndexCarries(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"1.2.3":         "1.2.3",
		"v1.2.3":        "1.2.3",
		"90.0.0":        "90.0.0",
		"2.0.0-rc.1":    "2.0.0-rc.1",
		"1.2.3+build.5": "1.2.3+build.5",
		// Trimmed on this side: the library refuses surrounding whitespace,
		// and an index entry is somebody else's text file.
		"  1.2.3  ": "1.2.3",
		// Accepted now and skipped as unparseable before. Helm reads a
		// partial version this way, so a chart published as `1.2` can be
		// reported as newer than a pin instead of being ignored.
		"1.2": "1.2.0",
		"1":   "1.0.0",
	} {
		t.Run(in, func(t *testing.T) {
			t.Parallel()

			got, err := ParseVersion(in)
			require.NoError(t, err)
			assert.Equal(t, want, got.String())
		})
	}
}

func TestParseVersion_Rejects(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"", "latest", "1.2.x", "a.b.c", "192.0.2.1", "-1.2.3"} {
		t.Run(in, func(t *testing.T) {
			t.Parallel()

			_, err := ParseVersion(in)
			require.Error(t, err)
			assert.Contains(t, err.Error(), in, "the failure does not quote what it could not read")
		})
	}
}

func TestParseVersion_SeesAPreRelease(t *testing.T) {
	t.Parallel()

	// LatestStable is built on this: the version policy is latest STABLE, and
	// build metadata is not a prerelease.
	for in, want := range map[string]bool{
		"1.2.3":       false,
		"1.2.3+meta":  false,
		"2.0.0-rc.1":  true,
		"2.0.0-beta":  true,
		"2.0.0-alpha": true,
	} {
		version, err := ParseVersion(in)
		require.NoError(t, err)
		assert.Equal(t, want, version.Prerelease() != "", in)
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
		// String comparison would put 10.8.4 before 9.0.0, which is exactly
		// the mistake parsing exists to prevent — Argo CD's chart is on 10.x.
		{"double-digit major", "10.8.4", "9.0.0", 1},
		{"v prefix is irrelevant", "v1.21.1", "1.21.1", 0},
		// A prerelease is OLDER than the release it precedes. Getting this
		// backwards reports a stable pin as outdated against a candidate that
		// has not shipped.
		{"release beats its own prerelease", "2.0.0", "2.0.0-rc.1", 1},
		{"prerelease before release", "2.0.0-rc.1", "2.0.0", -1},
		{"prerelease ordering", "2.0.0-rc.1", "2.0.0-rc.2", -1},
		{"prerelease of a newer number still wins", "2.0.0-rc.1", "1.9.9", 1},
		// The two the hand-written comparator got wrong: it compared
		// prerelease identifiers as text, so rc.9 sorted after rc.10. Semver
		// §11 compares numeric identifiers numerically.
		{"double-digit prerelease", "2.0.0-rc.9", "2.0.0-rc.10", -1},
		{"double-digit alpha", "1.0.0-alpha.2", "1.0.0-alpha.10", -1},
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
