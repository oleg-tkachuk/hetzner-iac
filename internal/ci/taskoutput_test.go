package ci

// What a task prints: the glyph vocabulary it shares with internal/pkg/pulumilog,
// and naming the layer again when a per-layer operation finishes.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// glyphVar matches a marker definition in the root taskfile, and goGlyph the
// matching constant in the logger.
var (
	glyphVar = regexp.MustCompile(`(?m)^  _([A-Z]+): '\{\{if \.NO_COLOR\}\}(.)\{\{else\}\}\{\{"\\x1b\[(\d+)m(.)\\x1b\[0m"\}\}`)
	goGlyph  = regexp.MustCompile(`(?m)^\t(Glyph\w+)\s+= "(.)"`)
	goColour = regexp.MustCompile(`(?m)^\t(colour\w+)\s+= "\\x1b\[(\d+)m"`)
)

// TestTaskGlyphs_MatchThePulumiLogger holds the two halves of one vocabulary
// equal.
//
// internal/pkg/pulumilog says its glyphs match the taskfiles "exactly" and that
// changing one without the other is how two tools stop looking like one — and
// nothing checked it. A task and the program it runs print into one terminal.
func TestTaskGlyphs_MatchThePulumiLogger(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	taskfile, err := os.ReadFile(filepath.Join(root, "Taskfile.yaml"))
	require.NoError(t, err)

	logger, err := os.ReadFile(filepath.Join(root, "internal", "pkg", "pulumilog", "pulumilog.go"))
	require.NoError(t, err)

	glyphs := map[string]string{}
	for _, found := range goGlyph.FindAllStringSubmatch(string(logger), -1) {
		glyphs[found[1]] = found[2]
	}

	require.NotEmpty(t, glyphs, "no glyph constants in pulumilog")

	colours := map[string]bool{}
	for _, found := range goColour.FindAllStringSubmatch(string(logger), -1) {
		colours[found[2]] = true
	}

	// The taskfile's marker name → the logger's constant. ERR has no pair:
	// a layer reports failure by returning an error, which Pulumi formats
	// itself, so pulumilog deliberately has no error glyph.
	for marker, constant := range map[string]string{
		"RUN":  "GlyphRunning",
		"OK":   "GlyphOK",
		"SKIP": "GlyphSkipped",
		"WARN": "GlyphWarning",
	} {
		glyph, colour := glyphOf(t, string(taskfile), marker)

		assert.Equal(t, glyphs[constant], glyph,
			"_%s in the taskfile and %s in pulumilog are different glyphs", marker, constant)
		assert.True(t, colours[colour],
			"_%s is coloured \\x1b[%sm in the taskfile, which pulumilog does not use", marker, colour)
	}
}

// glyphOf returns one marker's glyph and its ANSI colour code, and fails if
// the plain and coloured halves of the definition disagree.
func glyphOf(t *testing.T, taskfile, marker string) (glyph, colour string) {
	t.Helper()

	for _, found := range glyphVar.FindAllStringSubmatch(taskfile, -1) {
		if found[1] != marker {
			continue
		}

		require.Equal(t, found[2], found[4],
			"_%s prints one glyph without colour and another with it", marker)

		return found[2], found[3]
	}

	t.Fatalf("no _%s marker in the root taskfile", marker)

	return "", ""
}

// layerLoop is the shell loop that runs one Pulumi operation per layer.
const layerLoop = "for layer in"

// pulumiCall matches the operation a loop runs against a layer.
var pulumiCall = regexp.MustCompile(`pulumi --non-interactive --stack "\{\{\._PL_STACK\}\}" (\w+)`)

// operationsThatScroll are the Pulumi operations whose own output is long
// enough to carry the loop's header off the screen: a diff, a resource list, a
// summary and a duration each.
//
// `stack output` is deliberately not one of them. It prints a handful of lines
// that the header above them still covers, so a closing line there would be
// noise rather than an answer.
var operationsThatScroll = map[string]bool{
	"preview": true,
	"refresh": true,
	"up":      true,
	"destroy": true,
}

// closingLine is what names the layer again once its operation is done.
const closingLine = "{{._OK}} platform · ${layer}"

// TestLayerLoops_NameTheLayerWhenItFinishes keeps a result attributable.
//
// `layer=all` runs six operations in one command, and each prints its own
// diff, resource list and `Resources: N unchanged`. With only a header per
// layer, the summary a reader is looking at belongs to whichever header
// scrolled past — so the answer to "did 40-ingress change anything" was a
// scroll rather than a line.
func TestLayerLoops_NameTheLayerWhenItFinishes(t *testing.T) {
	t.Parallel()

	var checked int

	for _, path := range taskfiles(t) {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)

		for name, body := range tasksIn(string(raw)) {
			if !strings.Contains(body, layerLoop) {
				continue
			}

			var scrolls bool

			for _, call := range pulumiCall.FindAllStringSubmatch(body, -1) {
				if operationsThatScroll[call[1]] {
					scrolls = true
				}
			}

			if !scrolls {
				continue
			}

			checked++

			assert.Contains(t, body, closingLine,
				"%s in %s runs a Pulumi operation per layer and never names the layer again "+
					"after it. In layer=all the summary a reader is looking at then belongs to "+
					"whichever header scrolled past",
				name, filepath.Base(path))
		}
	}

	assert.Positive(t, checked,
		"no per-layer loop was examined, so this test proved nothing")
}
