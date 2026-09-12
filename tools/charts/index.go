// Command charts reports on the pinned Helm chart versions.
//
// `list` prints the registry. `outdated` compares each pin against the latest
// stable version in its upstream repository — the check that turns the pinning
// policy from a rule into something a machine enforces.
package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Version is a parsed chart version. Chart versions are semver in practice but
// spelled inconsistently — some repositories prefix a v, some publish
// prereleases into the same index — so they are parsed rather than compared as
// strings, where "10.8.4" sorts before "9.0.0".
type Version struct {
	Major, Minor, Patch int
	PreRelease          string
}

// ParseVersion accepts both the 1.2.3 and v1.2.3 spellings.
func ParseVersion(raw string) (Version, error) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(raw), "v")

	// Split off build metadata first: it takes no part in ordering.
	if plus := strings.IndexByte(trimmed, '+'); plus >= 0 {
		trimmed = trimmed[:plus]
	}

	var preRelease string
	if dash := strings.IndexByte(trimmed, '-'); dash >= 0 {
		preRelease = trimmed[dash+1:]
		trimmed = trimmed[:dash]
	}

	parts := strings.Split(trimmed, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("version %q is not major.minor.patch", raw)
	}

	numbers := make([]int, 3)

	for i, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil {
			return Version{}, fmt.Errorf("version %q: %q is not a number", raw, part)
		}

		if value < 0 {
			return Version{}, fmt.Errorf("version %q: %q is negative", raw, part)
		}

		numbers[i] = value
	}

	return Version{
		Major:      numbers[0],
		Minor:      numbers[1],
		Patch:      numbers[2],
		PreRelease: preRelease,
	}, nil
}

// IsPreRelease reports whether this is an alpha, beta or release candidate.
//
// Pins must not float onto one: the version policy is latest STABLE, and a
// repository index happily lists prereleases next to releases.
func (v Version) IsPreRelease() bool {
	return v.PreRelease != ""
}

// Compare orders two versions: -1 if v is older, 0 if equal, 1 if newer.
//
// A prerelease sorts BEFORE the release it precedes, as semver requires —
// 2.0.0-rc.1 is older than 2.0.0. Getting this backwards would report a stable
// pin as outdated against a candidate that has not shipped.
func (v Version) Compare(other Version) int {
	for _, pair := range [][2]int{
		{v.Major, other.Major},
		{v.Minor, other.Minor},
		{v.Patch, other.Patch},
	} {
		if pair[0] != pair[1] {
			if pair[0] < pair[1] {
				return -1
			}

			return 1
		}
	}

	switch {
	case v.PreRelease == other.PreRelease:
		return 0
	case v.PreRelease == "":
		return 1 // a release is newer than any prerelease of the same number
	case other.PreRelease == "":
		return -1
	case v.PreRelease < other.PreRelease:
		return -1
	default:
		return 1
	}
}

func (v Version) String() string {
	out := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.PreRelease != "" {
		out += "-" + v.PreRelease
	}

	return out
}

// LatestStable returns the newest non-prerelease version in the list.
//
// It returns false rather than an error when every candidate is a prerelease:
// that is a real state for a young chart, and not something to fail on.
func LatestStable(versions []string) (Version, bool) {
	stable := make([]Version, 0, len(versions))

	for _, raw := range versions {
		parsed, err := ParseVersion(raw)
		if err != nil {
			// A repository index can hold entries that are not semver at all.
			// Skipping them is correct: they cannot be the latest stable.
			continue
		}

		if parsed.IsPreRelease() {
			continue
		}

		stable = append(stable, parsed)
	}

	if len(stable) == 0 {
		return Version{}, false
	}

	// The maximum, rather than a sort and the last element: this asks for one
	// version and the order of the rest is not used for anything.
	return slices.MaxFunc(stable, Version.Compare), true
}
