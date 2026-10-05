// Package report is the layout every terminal report here shares —
// `platform:status`, `platform:drift` and `cluster:orphans` — so the three
// read as one tool: the same title, the same facts block, the same table with
// its mark after the first column, the same sections and the same closing line.
//
// The blocks are written in order and separated by one blank line:
//
//	◉ hetzner-iac · platform:drift · stack dev     Title
//
//	  checked   7 stacks · refresh preview          Facts
//
//	  STACK             CLOUD                       Table
//	  cluster        ✔  matches
//
//	  ▲ 40-ingress                                  Section
//	    ~ kubernetes:… name — spec.a
//
//	  ✔ every stack matches the cloud               Summary
//
// It holds no Pulumi import, like the report packages that use it.
package report

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
)

// Painter colours a string for the terminal.
type Painter func(code, s string) string

// Plain is the Painter for output that is not a terminal.
func Plain(_, s string) string { return s }

// ANSI is the Painter for a terminal.
func ANSI(code, s string) string { return "\x1b[" + code + "m" + s + "\x1b[0m" }

// noColorEnv is the convention the taskfiles and internal/pkg/pulumilog honour.
const noColorEnv = "NO_COLOR"

// PainterFor colours output to a terminal, unless NO_COLOR is set.
func PainterFor(file *os.File) Painter {
	if _, off := os.LookupEnv(noColorEnv); off || !term.IsTerminal(int(file.Fd())) {
		return Plain
	}

	return ANSI
}

// ANSI colour codes, the ones Taskfile.yaml's markers use.
const (
	Green  = "32"
	Cyan   = "36"
	Yellow = "33"
	Red    = "31"
	// Grey paints the "nothing here" mark only, never text: on a dark
	// background with a low-contrast palette it all but disappears.
	Grey = "90"
	Bold = "1"
)

// Marks, the repository's own vocabulary: the same glyphs Taskfile.yaml's
// _OK, _RUN, _WARN, _SKIP and _ERR print, so a report reads like the tasks
// around it.
const (
	MarkOK      = "✔"
	MarkRunning = "◉"
	MarkWarning = "▲"
	MarkNone    = "○"
	MarkFailed  = "✖"
)

// Layout: the margin every line under the title opens with, the gap between
// table columns and between a fact's label and value, and the separator of a
// line's parts.
const (
	Indent    = "  "
	ColumnGap = "  "
	Separator = " · "
)

// Name opens every title: the repository the reports belong to.
const Name = clusterspec.Name

// None stands in for a value a report does not have.
const None = "—"

// Cell is one coloured piece of text: a table cell, a mark.
type Cell struct {
	Text  string
	Color string
}

// Text is an uncoloured cell.
func Text(text string) Cell { return Cell{Text: text} }

// Mark is a mark in its colour.
func Mark(mark, color string) Cell { return Cell{Text: mark, Color: color} }

// The marks in the colour each always carries.
var (
	OK      = Mark(MarkOK, Green)
	Running = Mark(MarkRunning, Cyan)
	Warning = Mark(MarkWarning, Yellow)
	Nothing = Mark(MarkNone, Grey)
	Failed  = Mark(MarkFailed, Red)
)

// Fact is one line of the facts block.
type Fact struct {
	Label string
	Value string
}

// Table is a table whose second column is a row's mark.
type Table struct {
	// Headings, the mark column's included as "".
	Headings []string
	// Right holds the indexes of right-aligned, numeric, columns.
	Right map[int]bool
	Rows  [][]Cell
}

// Report builds a report block by block.
type Report struct {
	out    strings.Builder
	paint  Painter
	blocks int
}

// New starts a report.
func New(paint Painter) *Report {
	return &Report{paint: paint}
}

// Paint colours text with the report's painter.
func (r *Report) Paint(color, text string) string {
	if color == "" || text == "" {
		return text
	}

	return r.paint(color, text)
}

// block separates a block from the one before it.
func (r *Report) block() {
	if r.blocks > 0 {
		r.out.WriteString("\n")
	}

	r.blocks++
}

// Title names the report: the task that prints it, and the stack.
func (r *Report) Title(task, stack string) {
	r.block()
	fmt.Fprintf(&r.out, "%s %s%sstack %s\n",
		r.Paint(Cyan, MarkRunning), r.Paint(Bold, Name+Separator+task), Separator, stack)
}

// Facts writes labelled values, the values aligned in one column.
func (r *Report) Facts(facts []Fact) {
	if len(facts) == 0 {
		return
	}

	r.block()

	width := 0
	for _, fact := range facts {
		width = max(width, utf8.RuneCountInString(fact.Label))
	}

	for _, fact := range facts {
		// Pad before painting: an escape sequence has width in a format verb
		// and none on screen, so padding a painted label eats its own gap.
		label := pad(fact.Label, width)
		r.out.WriteString(Indent + r.Paint(Bold, label) + ColumnGap + fact.Value + "\n")
	}
}

// Table writes a table. The last column runs on, so a long note does not
// stretch the columns before it.
func (r *Report) Table(table Table) {
	r.block()

	widths := make([]int, len(table.Headings))
	head := make([]Cell, len(table.Headings))

	for i, heading := range table.Headings {
		widths[i] = utf8.RuneCountInString(heading)
		head[i] = Cell{Text: heading, Color: Bold}
	}

	for _, row := range table.Rows {
		for i, cell := range row {
			if i < len(row)-1 && i < len(widths) {
				widths[i] = max(widths[i], utf8.RuneCountInString(cell.Text))
			}
		}
	}

	r.row(head, widths, table.Right)

	for _, row := range table.Rows {
		r.row(row, widths, table.Right)
	}
}

func (r *Report) row(cells []Cell, widths []int, right map[int]bool) {
	var line strings.Builder

	line.WriteString(Indent)

	for i, cell := range cells {
		last := i == len(cells)-1
		gap := strings.Repeat(" ", max(widths[i]-utf8.RuneCountInString(cell.Text), 0))
		text := r.Paint(cell.Color, cell.Text)

		switch {
		case right[i]:
			line.WriteString(gap + text)
		case last:
			line.WriteString(text)
		default:
			line.WriteString(text + gap)
		}

		if !last {
			line.WriteString(ColumnGap)
		}
	}

	r.out.WriteString(strings.TrimRight(line.String(), " ") + "\n")
}

// Section writes a marked heading and the lines under it, indented once more.
func (r *Report) Section(mark Cell, heading string, lines []string) {
	r.block()
	r.out.WriteString(Indent + r.Paint(mark.Color, mark.Text) + " " + r.Paint(Bold, heading) + "\n")

	for _, line := range lines {
		r.out.WriteString(Indent + Indent + line + "\n")
	}
}

// Lines writes a block of plain lines.
func (r *Report) Lines(lines []string) {
	if len(lines) == 0 {
		return
	}

	r.block()

	for _, line := range lines {
		r.out.WriteString(Indent + line + "\n")
	}
}

// Summary closes the report: the verdict, in the mark's colour, and what to
// do about it, under the verdict's text.
func (r *Report) Summary(mark Cell, verdict string, hints ...string) {
	r.block()
	r.out.WriteString(Indent + r.Paint(mark.Color, mark.Text+" "+verdict) + "\n")

	for _, hint := range hints {
		r.out.WriteString(Indent + "  " + hint + "\n")
	}
}

// String is the report so far.
func (r *Report) String() string {
	return r.out.String()
}

// Columns lays out rows of plain text in aligned columns, the last running
// on, the ones in right aligned: the lines of a Section.
func Columns(rows [][]string, right map[int]bool) []string {
	widths := map[int]int{}

	for _, row := range rows {
		for i, text := range row[:max(len(row)-1, 0)] {
			widths[i] = max(widths[i], utf8.RuneCountInString(text))
		}
	}

	lines := make([]string, 0, len(rows))

	for _, row := range rows {
		parts := make([]string, len(row))
		for i, text := range row {
			switch {
			case right[i]:
				text = strings.Repeat(" ", max(widths[i]-utf8.RuneCountInString(text), 0)) + text
			case i < len(row)-1:
				text = pad(text, widths[i])
			}

			parts[i] = text
		}

		lines = append(lines, strings.TrimRight(strings.Join(parts, ColumnGap), " "))
	}

	return lines
}

// Count is n and the noun in the number it agrees with: "1 stack", "7 stacks".
func Count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}

	return strconv.Itoa(n) + " " + many
}

// pad fills text to width runes, so a multi-byte mark counts once.
func pad(text string, width int) string {
	return text + strings.Repeat(" ", max(width-utf8.RuneCountInString(text), 0))
}
