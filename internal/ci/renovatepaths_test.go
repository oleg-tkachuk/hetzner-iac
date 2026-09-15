package ci

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// anchoredPattern strips Renovate's regex delimiters and the anchors, leaving
// the literal path a pattern watches. Both patterns in this repository are
// fully literal apart from one character class, which is why this can be a
// string operation rather than a regex engine.
var anchoredPattern = regexp.MustCompile(`^/\^(.+)\$/$`)

// TestRenovatePatterns_WatchPathsThatExist closes the hole that makes a
// renamed directory silent.
//
// A customManager whose managerFilePatterns match nothing is not an error to
// Renovate: it finds no dependency there, proposes no update, and says
// nothing — for ever. renovate.json's own comment names that failure for the
// regex inside the manager, and a test holds that half. Nothing held the
// path, so moving `pkg/charts/registry.go` would have stopped every chart
// update with no signal anywhere.
//
// Found while costing the move of pkg/ under internal/, which is exactly the
// change that would have triggered it.
func TestRenovatePatterns_WatchPathsThatExist(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	raw, err := os.ReadFile(filepath.Join(root, ".github", "renovate.json"))
	require.NoError(t, err)

	var config struct {
		CustomManagers []struct {
			ManagerFilePatterns []string `json:"managerFilePatterns"`
		} `json:"customManagers"`
	}

	require.NoError(t, json.Unmarshal(raw, &config))
	require.NotEmpty(t, config.CustomManagers, "no custom managers; this test is checking nothing")

	var checked int

	for _, manager := range config.CustomManagers {
		require.NotEmpty(t, manager.ManagerFilePatterns, "a custom manager watches no path at all")

		for _, pattern := range manager.ManagerFilePatterns {
			found := anchoredPattern.FindStringSubmatch(pattern)
			require.NotNil(t, found,
				"%q is not the anchored form this test can read; every pattern here is /^…$/", pattern)

			// The literal prefix, up to the first regex metacharacter. For a
			// pattern naming one file that is the whole path; for one naming a
			// directory of files it is the directory, which is what has to
			// exist.
			literal := found[1]
			if cut := strings.IndexAny(literal, `[](){}|+*?`); cut >= 0 {
				literal = literal[:cut]
			}

			literal = strings.ReplaceAll(literal, `\.`, ".")
			literal = strings.TrimSuffix(literal, "/")

			checked++

			_, err := os.Stat(filepath.Join(root, literal))
			assert.NoError(t, err,
				"a custom manager watches %q and %q does not exist, so Renovate reads no "+
					"dependency there and proposes no update, silently", pattern, literal)
		}
	}

	assert.Positive(t, checked, "no file patterns read; this test is checking nothing")
}
