package repo

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isoDate matches a calendar date written the way this repository used to
// stamp its own findings.
var isoDate = regexp.MustCompile(`\b20[0-9]{2}-[0-9]{2}-[0-9]{2}\b`)

// datesThatAreData are the lines where a date is the subject rather than a
// timestamp on our own work, matched by their distinguishing text.
//
// Both survive because git history cannot answer what they say. One is a
// deprecation date in somebody else's API; the other is a date inside a
// snapshot description Hetzner generates, quoted as sample output — removing
// it would make the example wrong.
var datesThatAreData = []string{
	"/v1/datacenters",
	"snapshot 2026-09-11T00:18:18Z",
}

// TestNoDatesInProse keeps "measured on <date>" out of comments and
// documentation.
//
// Asked for after the tree had collected thirty-four of them — "Measured on
// …", "Checked on …", "done …" — each stamping when somebody learned
// something. Git history records that, per line, without anybody maintaining
// it, and a date in prose has two costs: it goes stale silently, and it
// invites the reader to weigh the finding by its age rather than by whether
// the code still does what it says.
//
// The examples above are written with an ellipsis rather than with real dates
// on purpose: this file excludes itself from the scan, so a date quoted here
// would pass, and a rule whose own statement breaks it is a rule nobody
// believes.
//
// The finding itself stays. "Measured: the load balancer came up with zero
// targets" is the sentence worth having; the date on it was never the part
// that mattered.
func TestNoDatesInProse(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	var offences []string

	require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".cache", "node_modules", "coverage", ".backups":
				return filepath.SkipDir
			// Architecture decision records keep their Date, and the reason
			// this rule exists is why. A date in prose is redundant BECAUSE
			// git records it — and docs/adr is not in git, so nothing would
			// record it. `**Date:**` is also a structural field of the ADR
			// format rather than a note on when somebody measured something.
			case "adr":
				return filepath.SkipDir
			}

			return nil
		}

		switch filepath.Ext(path) {
		case ".go", ".md", ".yaml", ".yml", ".json", ".tmpl":
		default:
			return nil
		}

		// This file names the pattern it looks for.
		if filepath.Base(path) == "dates_test.go" {
			return nil
		}

		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}

		for i, line := range strings.Split(string(raw), "\n") {
			if !isoDate.MatchString(line) || isData(line) {
				continue
			}

			offences = append(offences,
				filepath.ToSlash(path)+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
		}

		return nil
	}))

	assert.Empty(t, offences,
		"a date in prose. Git history records when something was measured, per line and "+
			"without maintenance — keep the finding and drop the date:\n  %s",
		strings.Join(offences, "\n  "))
}

// isData reports whether a date on this line is the subject rather than a
// timestamp on our own work.
func isData(line string) bool {
	for _, marker := range datesThatAreData {
		if strings.Contains(line, marker) {
			return true
		}
	}

	return false
}
