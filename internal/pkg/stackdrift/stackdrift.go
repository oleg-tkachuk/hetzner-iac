// Package stackdrift is the report behind `task platform:drift`: which
// resources the cloud no longer agrees with, stack by stack. It holds the
// judgements and the rendering; tools/stackdrift reads the stacks.
//
// Drift is the cloud moving while the code and the state did not — a label
// edited in the Hetzner console, a field patched with kubectl. `pulumi
// preview` cannot see it: it compares the program with the state, and the
// state is what the last apply wrote. Only a refresh reads the cloud, and a
// refresh preview reads it without writing anything back.
package stackdrift

import (
	"fmt"
	"io"
	"strings"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/stackstatus"
)

// The operations a refresh preview reports for a resource that differs from
// the cloud. Pulumi spells them; a refresh plans nothing else that matters.
const (
	// OpUpdate is a resource whose live fields differ from the state.
	OpUpdate = "update"
	// OpDelete is a resource the state holds and the cloud no longer has.
	OpDelete = "delete"
)

// indent is the report's left margin, the one platform:status uses.
const indent = "  "

// Change is one resource the cloud no longer agrees with.
type Change struct {
	Type   string
	Name   string
	Op     string
	Fields []string
}

// Project is one project's stack and what its refresh preview found.
type Project struct {
	Name     string
	HasStack bool
	Changes  []Change
	Err      error
}

// Drifted reports whether any project found drift or could not be read. An
// unread stack counts: a report that cannot say "no drift" must not exit as
// though it had.
func Drifted(projects []Project) bool {
	for _, project := range projects {
		if project.Err != nil || len(project.Changes) > 0 {
			return true
		}
	}

	return false
}

// Render writes the whole report at once.
func Render(out io.Writer, stack string, projects []Project, paint stackstatus.Painter) error {
	var report strings.Builder

	fmt.Fprintf(&report, "%s %s\n\n",
		paint(stackstatus.Cyan, stackstatus.MarkRunning), paint(stackstatus.Bold, "drift · stack "+stack))

	width := 0
	for _, project := range projects {
		width = max(width, len(project.Name))
	}

	drifted, unread := 0, 0

	for _, project := range projects {
		name := fmt.Sprintf("%-*s", width, project.Name)

		switch {
		case project.Err != nil:
			unread++

			fmt.Fprintf(&report, "%s%s  %s  %v\n", indent, paint(stackstatus.Red, stackstatus.MarkFailed), name, project.Err)
		case !project.HasStack:
			fmt.Fprintf(&report, "%s%s  %s  no %s stack\n", indent, paint(stackstatus.Grey, stackstatus.MarkNone), name, stack)
		case len(project.Changes) == 0:
			fmt.Fprintf(&report, "%s%s  %s  matches the cloud\n", indent, paint(stackstatus.Green, stackstatus.MarkOK), name)
		default:
			drifted += len(project.Changes)

			fmt.Fprintf(&report, "%s%s  %s  %d resource(s) differ from the cloud\n",
				indent, paint(stackstatus.Yellow, stackstatus.MarkWarning), name, len(project.Changes))

			for _, change := range project.Changes {
				fmt.Fprintf(&report, "%s%s   %s\n", indent, indent, describe(change))
			}
		}
	}

	report.WriteString("\n" + indent + summary(drifted, unread, paint) + "\n")

	if _, err := io.WriteString(out, report.String()); err != nil {
		return fmt.Errorf("write drift report: %w", err)
	}

	return nil
}

func describe(change Change) string {
	subject := change.Type + " " + change.Name

	switch {
	case change.Op == OpDelete:
		return "- " + subject + " — gone from the cloud"
	case len(change.Fields) > 0:
		return "~ " + subject + " — " + strings.Join(change.Fields, ", ")
	default:
		return "~ " + subject
	}
}

func summary(drifted, unread int, paint stackstatus.Painter) string {
	switch {
	case unread > 0:
		return paint(stackstatus.Red, fmt.Sprintf("%d stack(s) could not be read, so drift is unknown there", unread))
	case drifted > 0:
		return paint(stackstatus.Yellow, fmt.Sprintf(
			"%d resource(s) changed outside Pulumi: a refresh adopts the change, an apply puts it back", drifted))
	default:
		return paint(stackstatus.Green, "every stack matches the cloud")
	}
}
