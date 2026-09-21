// Command charts reports on the pinned Helm chart versions.
//
// `list` prints the registry. `outdated` compares each pin against the latest
// stable version in its upstream repository — the check that turns the pinning
// policy from a rule into something a machine enforces.
package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// ParseVersion reads a chart version, which is semver spelled inconsistently:
// some repositories prefix a v, some leave the patch off, some publish
// prereleases into the same index.
//
// Masterminds/semver rather than a parser written here, and this is the
// library that DEFINES the answer rather than merely one that can produce it:
// Helm resolves chart versions with it, so "the latest stable in the index"
// means here exactly what `helm upgrade` would mean by it.
//
// The version written by hand got the specification wrong in one place nothing
// had reached yet. It compared prerelease identifiers as text, so 2.0.0-rc.9
// sorted AFTER 2.0.0-rc.10 — semver §11 compares numeric identifiers
// numerically. LatestStable drops prereleases before they are ever compared,
// which is the only reason that never surfaced; it is also exactly the kind of
// clause a hand-written parser is wrong about and a library is not.
//
// NewVersion rather than StrictNewVersion, because the lenient one is what
// Helm calls and it takes the spellings an index actually carries: a leading
// v, and a version with the patch left off, which it reads as .0. The
// hand-written parser refused both of those, so a chart published as `1.2` was
// skipped as unparseable and could not be reported as newer than a pin.
//
// TrimSpace stays on this side. The library refuses a version with surrounding
// whitespace outright, and an index entry is somebody else's text file.
func ParseVersion(raw string) (*semver.Version, error) {
	version, err := semver.NewVersion(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("version %q: %w", raw, err)
	}

	return version, nil
}

// LatestStable returns the newest non-prerelease version in the list.
//
// It returns false rather than an error when every candidate is a prerelease:
// that is a real state for a young chart, and not something to fail on.
//
// This is the part that is ours rather than the library's — the version policy
// is latest STABLE, and a repository index happily lists prereleases next to
// releases.
func LatestStable(versions []string) (*semver.Version, bool) {
	stable := make([]*semver.Version, 0, len(versions))

	for _, raw := range versions {
		parsed, err := ParseVersion(raw)
		if err != nil {
			// A repository index can hold entries that are not semver at all.
			// Skipping them is correct: they cannot be the latest stable.
			continue
		}

		if parsed.Prerelease() != "" {
			continue
		}

		stable = append(stable, parsed)
	}

	if len(stable) == 0 {
		return nil, false
	}

	// The maximum, rather than a sort and the last element: this asks for one
	// version and the order of the rest is not used for anything.
	return slices.MaxFunc(stable, func(a, b *semver.Version) int { return a.Compare(b) }), true
}
