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

func TestLine_MarkerThenScopeThenFields(t *testing.T) {
	t.Parallel()

	// Same shape as taskfiles/: glyph and word, scope, then fields joined by a
	// middle dot. Output from `task` and from `pulumi up` should read as one
	// tool.
	got := plain("observability").line(markRunning, "loki", "chart 7.3.0 → observability")

	assert.Equal(t, "◉ start · observability · loki · chart 7.3.0 → observability", got)
}

func TestLine_EveryMarkerPutsTheScopeInOneColumn(t *testing.T) {
	t.Parallel()

	// The words differ in length; the padding is what keeps the fields of a
	// mixed run of lines aligned, as taskfiles/ prints them.
	for name, tc := range map[string]struct {
		mark marker
		want string
	}{
		"start": {markRunning, "◉ start · layer"},
		"ok":    {markOK, "✔ ok    · layer"},
		"skip":  {markSkipped, "○ skip  · layer"},
		"warn":  {markWarning, "▲ warn  · layer"},
	} {
		assert.Equal(t, tc.want, plain("layer").line(tc.mark, "", ""), name)
	}
}

func TestLine_OmitsEmptyFieldsRatherThanLeavingDanglingSeparators(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		component, detail, want string
	}{
		"both":         {"bucket", "created", "✔ ok    · layer · bucket · created"},
		"no detail":    {"bucket", "", "✔ ok    · layer · bucket"},
		"no component": {"", "created", "✔ ok    · layer · created"},
		"neither":      {"", "", "✔ ok    · layer"},
	} {
		got := plain("layer").line(markOK, tc.component, tc.detail)
		assert.Equal(t, tc.want, got, name)
	}
}

func TestLine_ColoursOnlyTheGlyphAndItsWord(t *testing.T) {
	t.Parallel()

	// The text stays uncoloured on purpose: a line whose body carries escape
	// codes is unreadable once it is pasted into an issue or grepped. The
	// padding is outside the colour too, as taskfiles/ prints it.
	got := coloured("cluster-services").line(markSkipped, "cluster-issuer", "acmeEmail unset")

	painted := colourGrey + GlyphSkipped + " " + WordSkipped + colourReset

	assert.Equal(t, "\x1b[90m○ skip\x1b[0m  · cluster-services · cluster-issuer · acmeEmail unset", got)
	assert.Equal(t, 1, strings.Count(got, colourReset), "exactly one reset, right after the word")
	assert.NotContains(t, strings.TrimPrefix(got, painted), "\x1b[")
}

func TestLine_WithoutColourCarriesNoEscapeCodes(t *testing.T) {
	t.Parallel()

	// NO_COLOR has to produce output that is safe to diff and to grep.
	got := plain("cluster-services").line(markWarning, "issuer", "no email")

	assert.NotContains(t, got, "\x1b[")
	assert.Equal(t, "▲ warn  · cluster-services · issuer · no email", got)
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

	assert.Equal(t, "ok", WordOK)
	assert.Equal(t, "start", WordRunning)
	assert.Equal(t, "warn", WordWarning)
	assert.Equal(t, "skip", WordSkipped)

	for _, word := range []string{WordOK, WordRunning, WordWarning, WordSkipped} {
		assert.LessOrEqual(t, len(word), wordWidth, "%q is wider than the column every word is padded to", word)
	}

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
