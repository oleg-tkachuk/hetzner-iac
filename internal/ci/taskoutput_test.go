package ci

// What a task prints: the marker vocabulary it shares with internal/pkg/pulumilog,
// and naming the layer again when a per-layer operation finishes.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/ci/citest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// markerVar matches a marker definition in an entry point's vars — a glyph,
// a word, the padding after it and the `·` that opens the scope, once without
// colour and once with it — and goGlyph, goWord and goColour the matching
// constants in the logger.
var (
	markerVar = regexp.MustCompile(
		`(?m)^  _([A-Z]+): '\{\{if \.NO_COLOR\}\}(.) (\w+)( +)·` +
			`\{\{else\}\}\{\{"\\x1b\[(\d+)m(.) (\w+)\\x1b\[0m( +)·"\}\}\{\{end\}\}'$`)
	goGlyph  = regexp.MustCompile(`(?m)^\t(Glyph\w+)\s+= "(.)"`)
	goWord   = regexp.MustCompile(`(?m)^\t(Word\w+)\s+= "(\w+)"`)
	goColour = regexp.MustCompile(`(?m)^\t(colour\w+)\s+= "\\x1b\[(\d+)m"`)
)

// taskMarker is one marker definition, read from a taskfile.
type taskMarker struct {
	glyph, word, colour string
	// width is the word and its padding together: where the `·` falls.
	width int
}

// TestTaskGlyphs_MatchThePulumiLogger holds the two halves of one vocabulary
// equal.
//
// internal/pkg/pulumilog says its glyphs match the taskfiles "exactly" and that
// changing one without the other is how two tools stop looking like one — and
// nothing checked it. A task and the program it runs print into one terminal.
func TestTaskGlyphs_MatchThePulumiLogger(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	logger, err := os.ReadFile(filepath.Join(root, "internal", "pkg", "pulumilog", "pulumilog.go"))
	require.NoError(t, err)

	glyphs := map[string]string{}
	for _, found := range goGlyph.FindAllStringSubmatch(string(logger), -1) {
		glyphs[found[1]] = found[2]
	}

	words := map[string]string{}
	for _, found := range goWord.FindAllStringSubmatch(string(logger), -1) {
		words[found[1]] = found[2]
	}

	require.NotEmpty(t, glyphs, "no glyph constants in pulumilog")
	require.Len(t, words, len(glyphs), "pulumilog has a glyph without its word, or a word without its glyph")

	colours := map[string]bool{}
	for _, found := range goColour.FindAllStringSubmatch(string(logger), -1) {
		colours[found[2]] = true
	}

	// The taskfile's marker name → the suffix of the logger's Glyph and Word
	// constants. ERR has no pair: a layer reports failure by returning an
	// error, which Pulumi formats itself, so pulumilog deliberately has no
	// error marker.
	markers := map[string]string{
		"RUN":  "Running",
		"OK":   "OK",
		"SKIP": "Skipped",
		"WARN": "Warning",
	}

	// Both entry points, because both print. Each defines the markers its own
	// tasks use rather than all of them, so the check is per marker DECLARED
	// — and the union below has to cover every constant the logger has, or a
	// marker could quietly stop being defined anywhere.
	//
	// Forgetting one is silent: Task renders an undefined variable as the
	// empty string, so a status line loses its glyph and prints anyway.
	defined := map[string]bool{}

	for _, name := range []string{"Taskfile.yaml", citest.DevTaskfile} {
		raw, readErr := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, readErr, name)

		var declared int

		for marker, suffix := range markers {
			found, ok := markerOf(t, string(raw), marker)
			if !ok {
				continue
			}

			declared++
			defined[marker] = true

			assert.Equal(t, glyphs["Glyph"+suffix], found.glyph,
				"_%s in %s and Glyph%s in pulumilog are different glyphs", marker, name, suffix)
			assert.Equal(t, words["Word"+suffix], found.word,
				"_%s in %s and Word%s in pulumilog are different words", marker, name, suffix)
			assert.True(t, colours[found.colour],
				"_%s is coloured \\x1b[%sm in %s, which pulumilog does not use",
				marker, found.colour, name)
		}

		assert.Positive(t, declared,
			"%s defines no status marker, so every status line it prints is missing its glyph",
			name)
	}

	for marker := range markers {
		assert.True(t, defined[marker],
			"no entry point defines _%s any more, and pulumilog still has its pair", marker)
	}
}

// markerOf returns one marker as the taskfile defines it, whether it defines
// it at all, and fails if the plain and coloured halves of the definition
// disagree.
func markerOf(t *testing.T, taskfile, marker string) (taskMarker, bool) {
	t.Helper()

	for _, match := range markerVar.FindAllStringSubmatch(taskfile, -1) {
		if match[1] != marker {
			continue
		}

		plain := match[2] + " " + match[3] + match[4]
		painted := match[6] + " " + match[7] + match[8]
		require.Equal(t, plain, painted,
			"_%s prints one marker without colour and another with it", marker)

		return taskMarker{
			glyph:  match[2],
			word:   match[3],
			colour: match[5],
			width:  len(match[3]) + len(match[4]),
		}, true
	}

	return taskMarker{}, false
}

// statusMarkers are every marker an entry point may define.
var statusMarkers = []string{"RUN", "OK", "SKIP", "WARN", "ERR"}

// TestTaskMarkers_PutTheScopeInOneColumn holds the padding: the words differ
// in length, and a marker padded to another width than the rest starts its
// scope one column off from every line around it.
func TestTaskMarkers_PutTheScopeInOneColumn(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	for _, name := range []string{"Taskfile.yaml", citest.DevTaskfile} {
		raw, err := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, err)

		widths := map[int][]string{}

		for _, marker := range statusMarkers {
			if found, ok := markerOf(t, string(raw), marker); ok {
				widths[found.width] = append(widths[found.width], marker)
			}
		}

		require.NotEmpty(t, widths, "%s defines no status marker in the form this test reads", name)
		assert.Len(t, widths, 1, "%s pads its markers to different widths: %v", name, widths)
	}
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

	for _, path := range citest.Taskfiles(t, filepath.Join("..", "..")) {
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

// statusMark matches a mark in the status report, and statusColour one of its
// colour codes.
var (
	statusMark   = regexp.MustCompile(`(?m)^\t(Mark\w+)\s+= "(.)"`)
	statusColour = regexp.MustCompile(`(?m)^\t(\w+)\s+= "(\d+)"`)
)

// TestStatusMarks_MatchTheTaskfile holds the third copy of the vocabulary.
//
// internal/pkg/report cannot import pulumilog — the reports that use it keep
// Pulumi out — so it spells the glyphs and colours again, and this is what
// keeps platform:status, platform:drift and cluster:orphans printing the marks
// the tasks around them print. Only the glyph: a report's marks are table
// cells and section heads, where the word would widen a column for nothing.
func TestStatusMarks_MatchTheTaskfile(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	report, err := os.ReadFile(filepath.Join(root, "internal", "pkg", "report", "report.go"))
	require.NoError(t, err)

	taskfile, err := os.ReadFile(filepath.Join(root, "Taskfile.yaml"))
	require.NoError(t, err)

	marks := map[string]string{}
	for _, found := range statusMark.FindAllStringSubmatch(string(report), -1) {
		marks[found[1]] = found[2]
	}

	colours := map[string]string{}
	for _, found := range statusColour.FindAllStringSubmatch(string(report), -1) {
		colours[found[1]] = found[2]
	}

	// The taskfile's marker → the report's mark and the colour it paints it.
	pairs := map[string][2]string{
		"OK":   {"MarkOK", "Green"},
		"RUN":  {"MarkRunning", "Cyan"},
		"WARN": {"MarkWarning", "Yellow"},
		"SKIP": {"MarkNone", "Grey"},
		"ERR":  {"MarkFailed", "Red"},
	}

	require.Len(t, marks, len(pairs), "report declares a mark this test does not pair with a marker")

	for marker, pair := range pairs {
		found, ok := markerOf(t, string(taskfile), marker)
		require.True(t, ok, "Taskfile.yaml no longer defines _%s", marker)

		assert.Equal(t, found.glyph, marks[pair[0]], "_%s and report.%s are different glyphs", marker, pair[0])
		assert.Equal(t, found.colour, colours[pair[1]], "_%s and report.%s are different colours", marker, pair[1])
	}
}
