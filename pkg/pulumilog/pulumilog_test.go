package pulumilog

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The format is what this package exists to decide, so it is what the tests
// pin. Constructing a Logger directly rather than through New: New needs a
// Pulumi context, and the context contributes nothing but the scope string.
func plain(scope string) *Logger {
	return &Logger{scope: scope, colour: false}
}

func coloured(scope string) *Logger {
	return &Logger{scope: scope, colour: true}
}

func TestLine_GlyphThenScopeThenFields(t *testing.T) {
	t.Parallel()

	// Same shape as taskfiles/: glyph, scope, then fields joined by a middle
	// dot. Output from `task` and from `pulumi up` should read as one tool.
	got := plain("observability").line(GlyphRunning, colourCyan, "loki", "chart 7.3.0 → observability")

	assert.Equal(t, "◉ observability · loki · chart 7.3.0 → observability", got)
}

func TestLine_OmitsEmptyFieldsRatherThanLeavingDanglingSeparators(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		component, detail, want string
	}{
		"both":         {"bucket", "created", "✔ layer · bucket · created"},
		"no detail":    {"bucket", "", "✔ layer · bucket"},
		"no component": {"", "created", "✔ layer · created"},
		"neither":      {"", "", "✔ layer"},
	} {
		got := plain("layer").line(GlyphOK, colourGreen, tc.component, tc.detail)
		assert.Equal(t, tc.want, got, name)
	}
}

func TestLine_ColoursOnlyTheGlyph(t *testing.T) {
	t.Parallel()

	// The text stays uncoloured on purpose: a line whose body carries escape
	// codes is unreadable once it is pasted into an issue or grepped.
	got := coloured("core").line(GlyphSkipped, colourGrey, "cluster-issuer", "acmeEmail unset")

	assert.Equal(t, "\x1b[90m○\x1b[0m core · cluster-issuer · acmeEmail unset", got)
	assert.Equal(t, 1, strings.Count(got, colourReset), "exactly one reset, right after the glyph")
	assert.NotContains(t, strings.TrimPrefix(got, colourGrey+GlyphSkipped+colourReset), "\x1b[")
}

func TestLine_WithoutColourCarriesNoEscapeCodes(t *testing.T) {
	t.Parallel()

	// NO_COLOR has to produce output that is safe to diff and to grep.
	got := plain("core").line(GlyphWarning, colourYellow, "issuer", "no email")

	assert.NotContains(t, got, "\x1b[")
	assert.Equal(t, "▲ core · issuer · no email", got)
}

func TestColourEnabled_HonoursNoColor(t *testing.T) {
	// t.Setenv forbids t.Parallel.
	t.Setenv("NO_COLOR", "1")
	assert.False(t, colourEnabled())

	// Empty but set still means no colour: the convention is presence, not
	// truthiness, and `NO_COLOR=` from a shell script is a deliberate opt-out.
	t.Setenv("NO_COLOR", "")
	assert.False(t, colourEnabled())
}

func TestColourEnabled_DefaultsOn(t *testing.T) {
	// t.Setenv registers the restore; unsetting afterwards leaves the variable
	// absent for this test and restored for the next.
	t.Setenv("NO_COLOR", "1")
	require.NoError(t, os.Unsetenv("NO_COLOR"))

	// A Pulumi program's stdout is never a terminal — the CLI captures it over
	// gRPC — so a TTY check would disable colour in exactly the interactive
	// run it is meant to serve. Presence of NO_COLOR is the only signal.
	assert.True(t, colourEnabled())
}

func TestGlyphs_MatchTheTaskfilesVocabulary(t *testing.T) {
	t.Parallel()

	// Pinned against taskfiles/: ✔ green, ◉ cyan, ▲ yellow, ○ grey. Changing
	// one here without changing it there is how two tools stop looking like
	// one, and that is not something a reader would notice in review.
	assert.Equal(t, "✔", GlyphOK)
	assert.Equal(t, "◉", GlyphRunning)
	assert.Equal(t, "▲", GlyphWarning)
	assert.Equal(t, "○", GlyphSkipped)

	assert.Equal(t, "\x1b[32m", colourGreen)
	assert.Equal(t, "\x1b[36m", colourCyan)
	assert.Equal(t, "\x1b[33m", colourYellow)
	assert.Equal(t, "\x1b[90m", colourGrey)
}

func TestLogger_NilAndContextlessLoggersDoNotPanic(t *testing.T) {
	t.Parallel()

	// A layer that logs before its context exists should not crash the run
	// over a log line.
	var nilLogger *Logger

	for name, logger := range map[string]*Logger{
		"nil":            nilLogger,
		"no context":     {scope: "layer"},
		"zero value ptr": {},
	} {
		assert.NotPanics(t, func() {
			logger.Step("component", "detail")
			logger.Done("component", "detail")
			logger.Skipped("component", "detail")
			logger.Warn("component", "detail %d", 1)
		}, name)
	}
}
