package report_test

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/report"
)

// ansiEscape strips the painter's codes, to measure what a terminal shows.
var ansiEscape = regexp.MustCompile("\x1b\\[[0-9;]*m")

// sample is every block once, in the order a report writes them.
func sample(paint report.Painter) string {
	r := report.New(paint)

	r.Title("platform:drift", "dev")
	r.Facts([]report.Fact{{Label: "checked", Value: "7 stacks"}, {Label: "HEAD", Value: "1495bea4"}})
	r.Table(report.Table{
		Headings: []string{"STACK", "", "COUNT", "NOTE"},
		Right:    map[int]bool{2: true},
		Rows: [][]report.Cell{
			{report.Text("cluster"), report.OK, report.Text("7"), report.Text("")},
			{report.Text("20-network-policy"), report.Warning, report.Text("31"), {Text: "behind", Color: report.Yellow}},
		},
	})
	r.Section(report.Warning, "20-network-policy", []string{"~ spec.description"})
	r.Summary(report.Warning, "1 stack differs", "an apply puts the code's back")

	return r.String()
}

func TestReport_BlocksInOrderOneBlankLineApart(t *testing.T) {
	t.Parallel()

	assert.Equal(t, ""+
		"◉ hetzner-iac · platform:drift · stack dev\n"+
		"\n"+
		"  checked  7 stacks\n"+
		"  HEAD     1495bea4\n"+
		"\n"+
		"  STACK                 COUNT  NOTE\n"+
		"  cluster            ✔      7\n"+
		"  20-network-policy  ▲     31  behind\n"+
		"\n"+
		"  ▲ 20-network-policy\n"+
		"    ~ spec.description\n"+
		"\n"+
		"  ▲ 1 stack differs\n"+
		"    an apply puts the code's back\n",
		sample(report.Plain))
}

// TestReport_PaintingDoesNotMoveAColumn: padding is measured on the text, not
// on its escape codes, and a multi-byte mark counts once.
func TestReport_PaintingDoesNotMoveAColumn(t *testing.T) {
	t.Parallel()

	painted := sample(report.ANSI)

	require.Contains(t, painted, "\x1b[", "the ANSI painter painted nothing")
	assert.Equal(t, sample(report.Plain), ansiEscape.ReplaceAllString(painted, ""))
}

func TestReport_EmptyBlocksWriteNothing(t *testing.T) {
	t.Parallel()

	r := report.New(report.Plain)
	r.Title("cluster:orphans", "dev")
	r.Facts(nil)
	r.Lines(nil)
	r.Summary(report.OK, "all 0 resources are claimed")

	assert.Equal(t, "◉ hetzner-iac · cluster:orphans · stack dev\n\n  ✔ all 0 resources are claimed\n", r.String())
}

func TestColumns(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{
		"pvc-gone    50 Gi  no PersistentVolume",
		"pvc-small  1.0 Gi  released",
	}, report.Columns([][]string{
		{"pvc-gone", "50 Gi", "no PersistentVolume"},
		{"pvc-small", "1.0 Gi", "released"},
	}, map[int]bool{1: true}))

	assert.Equal(t, []string{"~ cilium:Policy  spec.a, spec.b", "- hcloud:Server  gone"},
		report.Columns([][]string{{"~ cilium:Policy", "spec.a, spec.b"}, {"- hcloud:Server", "gone"}}, nil),
		"the last column runs on and nothing trails it")
}

func TestCount(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "1 stack", report.Count(1, "stack", "stacks"))
	assert.Equal(t, "0 stacks", report.Count(0, "stack", "stacks"))
	assert.Equal(t, "7 stacks", report.Count(7, "stack", "stacks"))
}

// TestPainterFor_HonoursNoColor is not parallel: it sets the environment.
func TestPainterFor_HonoursNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "")

	paint := report.PainterFor(os.Stdout)
	assert.Equal(t, "x", paint(report.Red, "x"), "NO_COLOR set, even empty, means no colour")
}

func TestPainterFor_PlainOffATerminal(t *testing.T) {
	t.Parallel()

	file, err := os.CreateTemp(t.TempDir(), "report")
	require.NoError(t, err)

	defer func() { require.NoError(t, file.Close()) }()

	assert.Equal(t, "x", report.PainterFor(file)(report.Red, "x"), "a file is not a terminal")
}

// TestMarks_CarryTheirColour holds each mark to the colour the taskfile's
// marker of the same glyph prints.
func TestMarks_CarryTheirColour(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		mark        report.Cell
		glyph, code string
	}{
		{report.OK, report.MarkOK, report.Green},
		{report.Running, report.MarkRunning, report.Cyan},
		{report.Warning, report.MarkWarning, report.Yellow},
		{report.Nothing, report.MarkNone, report.Grey},
		{report.Failed, report.MarkFailed, report.Red},
	} {
		assert.Equal(t, report.Cell{Text: tc.glyph, Color: tc.code}, tc.mark)
	}

	assert.False(t, strings.Contains(sample(report.ANSI), "\x1b["+report.Grey+"m"+"cluster"),
		"grey paints a mark, never text")
}
