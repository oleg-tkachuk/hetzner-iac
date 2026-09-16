package ci

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// adrDir holds the records and their index.
var adrDir = filepath.Join("..", "..", "docs", "adr")

// statusField is a record's own status line, `**Status:** Accepted`.
var statusField = regexp.MustCompile(`(?m)^\*\*Status:\*\*\s*(.+?)\s*$`)

// indexRow is a row of the index table: the number, the file it links, and
// the status column.
var indexRow = regexp.MustCompile(`(?m)^\|\s*\[(\d{4})\]\(([^)]+)\)\s*\|.*\|\s*([^|]+?)\s*\|\s*$`)

// recordFile matches the four digits a record's file name starts with.
var recordFile = regexp.MustCompile(`^(\d{4})-`)

// TestADRIndex_AgreesWithEveryRecord pairs each record's own status with the
// row the index gives it, in both directions.
//
// The index is what a reader opens first and the records are what they open
// next, so a row that disagrees with its file is worse than no row: it reports
// a decision as live when the file says it was replaced, or the other way
// round. Nothing generates either half.
//
// Measured, before the records were published: the table carried 0004 as
// Proposed long after that layout had been built, and described a directory
// tree the repository does not have.
func TestADRIndex_AgreesWithEveryRecord(t *testing.T) {
	t.Parallel()

	index, err := os.ReadFile(filepath.Join(adrDir, "README.md"))
	require.NoError(t, err)

	rows := map[string]struct{ file, status string }{}

	for _, row := range indexRow.FindAllStringSubmatch(string(index), -1) {
		rows[row[1]] = struct{ file, status string }{file: row[2], status: row[3]}
	}

	require.NotEmpty(t, rows, "the index has no rows, so this test proved nothing")

	entries, err := os.ReadDir(adrDir)
	require.NoError(t, err)

	var checked int

	for _, entry := range entries {
		number := recordFile.FindStringSubmatch(entry.Name())
		if number == nil {
			continue
		}

		checked++

		row, listed := rows[number[1]]
		if !assert.True(t, listed, "%s is not in the index table", entry.Name()) {
			continue
		}

		assert.Equal(t, entry.Name(), row.file,
			"the index links ADR-%s at %q, which is not its file", number[1], row.file)

		raw, readErr := os.ReadFile(filepath.Join(adrDir, entry.Name()))
		require.NoError(t, readErr)

		field := statusField.FindStringSubmatch(string(raw))
		require.NotNil(t, field, "%s has no **Status:** field", entry.Name())

		assert.Equal(t, statusOf(row.status), statusOf(field[1]),
			"%s says its status is %q and the index says %q",
			entry.Name(), field[1], row.status)

		delete(rows, number[1])
	}

	assert.Positive(t, checked, "no record was examined, so this test proved nothing")

	for number, row := range rows {
		assert.Fail(t, "the index has a row for a record that does not exist",
			"ADR-%s, linked at %q", number, row.file)
	}
}

// TestADRTitles_CarryTheirOwnNumber keeps a renumbered record from opening
// with somebody else's heading.
//
// The numbering was compacted once — one record folded into two others and the
// last one moved up — and a file whose name and heading disagree is invisible:
// the index links the file, the reader sees the heading, and the two are
// different records.
func TestADRTitles_CarryTheirOwnNumber(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(adrDir)
	require.NoError(t, err)

	var checked int

	for _, entry := range entries {
		number := recordFile.FindStringSubmatch(entry.Name())
		if number == nil {
			continue
		}

		raw, readErr := os.ReadFile(filepath.Join(adrDir, entry.Name()))
		require.NoError(t, readErr)

		heading, _, found := strings.Cut(string(raw), "\n")
		require.True(t, found, "%s has no heading", entry.Name())

		checked++

		assert.True(t, strings.HasPrefix(heading, "# ADR-"+number[1]+":"),
			"%s opens with %q, which does not name ADR-%s",
			entry.Name(), heading, number[1])
	}

	assert.Positive(t, checked, "no record was examined, so this test proved nothing")
}

// statusOf reduces a status to what has to match on both sides: the state,
// and the record that replaced this one when there is one.
//
// Comparing the strings whole was the first form and it failed on wording
// alone — a file writes `Superseded by [ADR-0005](0005-…md)` and a table cell
// has no room for the link.
func statusOf(status string) string {
	state := strings.ToLower(strings.Fields(status)[0])

	if superseding := regexp.MustCompile(`\d{4}`).FindString(status); superseding != "" {
		return state + " " + superseding
	}

	return state
}
