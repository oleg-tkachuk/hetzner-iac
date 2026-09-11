// Package pulumilog is the one place this codebase decides what a log line
// looks like.
//
// The vocabulary is deliberately the same as the taskfiles repository's, so
// output from `task` and output from `pulumi up` read as one tool rather than
// two: a glyph, then the scope, then dot-separated fields, then `→` for
// "became" or "went to".
//
//	◉ observability · loki · chart 7.3.0 → observability
//	✔ ingress · load-balancer · lb11 in hel1
//	○ core · cluster-issuer · acmeEmail unset, none created
//	▲ observability · alerting · alertmanager has no receiver
//
// Two channels, chosen per call rather than per site:
//
//   - Ephemeral for progress. Visible live in `pulumi up`, dropped from the
//     diagnostics afterwards, because a summary repeating one line per release
//     is a summary nobody reads.
//   - Permanent for anything an operator has to see after the run finished —
//     above all, a feature that silently did not get created. Those are the
//     lines that answer "why is there no Ingress" an hour later.
package pulumilog

import (
	"fmt"
	"os"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// Glyphs, matching taskfiles/ exactly. Changing one here without changing it
// there is how two tools stop looking like one.
const (
	GlyphOK      = "✔" // done
	GlyphRunning = "◉" // in progress
	GlyphWarning = "▲" // worth reading
	GlyphSkipped = "○" // deliberately not done
)

// No error glyph: a layer reports failure by returning an error, which Pulumi
// prints itself in its own format. taskfiles/ uses ✖ where it has to print the
// failure by hand; here there is nothing to print it from.

// ANSI colours, also matching taskfiles/.
const (
	colourGreen  = "\x1b[32m"
	colourCyan   = "\x1b[36m"
	colourYellow = "\x1b[33m"
	colourGrey   = "\x1b[90m"
	colourReset  = "\x1b[0m"
)

// Logger writes one layer's lines.
type Logger struct {
	ctx   *pulumi.Context
	scope string
	// colour is resolved once, at construction. Reading the environment per
	// call would let output change style halfway through a run.
	colour bool
}

// New builds a logger scoped to the running project, which is the layer name.
func New(ctx *pulumi.Context) *Logger {
	return &Logger{ctx: ctx, scope: ctx.Project(), colour: colourEnabled()}
}

// colourEnabled honours NO_COLOR, the convention taskfiles/ follows and the
// only signal available here.
//
// A TTY check would be wrong rather than merely unhelpful: a Pulumi program is
// a subprocess whose output the CLI captures over gRPC, so stdout is never a
// terminal and the check would disable colour every time, including in the
// interactive session it is meant to serve.
func colourEnabled() bool {
	_, set := os.LookupEnv("NO_COLOR")

	return !set
}

// Step reports work starting. Ephemeral.
func (l *Logger) Step(component, detail string) {
	l.info(GlyphRunning, colourCyan, component, detail, true)
}

// Done reports work finished. Ephemeral.
func (l *Logger) Done(component, detail string) {
	l.info(GlyphOK, colourGreen, component, detail, true)
}

// Skipped reports something deliberately not created, and survives the run.
//
// Permanent on purpose. A layer that quietly creates nothing because a config
// key is unset is the single most confusing thing this codebase can do, and
// the README documenting it does not help the person staring at the output.
func (l *Logger) Skipped(component, detail string) {
	l.info(GlyphSkipped, colourGrey, component, detail, false)
}

// Warn reports a configuration that will not do what it looks like it does.
// Permanent, and Pulumi counts it in the run's warning total.
func (l *Logger) Warn(component, format string, args ...any) {
	if l == nil || l.ctx == nil {
		return
	}

	line := l.line(GlyphWarning, colourYellow, component, fmt.Sprintf(format, args...))
	_ = l.ctx.Log.Warn(line, &pulumi.LogArgs{Ephemeral: false})
}

func (l *Logger) info(glyph, colour, component, detail string, ephemeral bool) {
	if l == nil || l.ctx == nil {
		return
	}

	_ = l.ctx.Log.Info(
		l.line(glyph, colour, component, detail),
		&pulumi.LogArgs{Ephemeral: ephemeral})
}

// line assembles one line: glyph, then scope and component separated by a
// middle dot, then the detail.
//
// The format is the point of this package, so the tests assert on it directly
// rather than through a Pulumi context, which does not capture diagnostics.
func (l *Logger) line(glyph, colour, component, detail string) string {
	if l.colour && colour != "" {
		glyph = colour + glyph + colourReset
	}

	line := glyph + " " + l.scope

	for _, field := range []string{component, detail} {
		if field != "" {
			line += " · " + field
		}
	}

	return line
}
