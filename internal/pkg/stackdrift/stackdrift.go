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

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/report"
)

// The operations a refresh preview reports for a resource that differs from
// the cloud. Pulumi spells them; a refresh plans nothing else that matters.
const (
	// OpUpdate is a resource whose live fields differ from the state.
	OpUpdate = "update"
	// OpDelete is a resource the state holds and the cloud no longer has.
	OpDelete = "delete"
)

// Task is the task that prints the report, which its title names.
const Task = "platform:drift"

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
func Render(out io.Writer, stack string, projects []Project, paint report.Painter) error {
	r := report.New(paint)

	r.Title(Task, stack)
	r.Facts([]report.Fact{{
		Label: "checked",
		Value: strings.Join([]string{
			report.Count(len(projects), "stack", "stacks"),
			"each state against the cloud, by refresh preview",
			"nothing written",
		}, report.Separator),
	}})

	rows := make([][]report.Cell, 0, len(projects))
	for _, project := range projects {
		mark, result := verdict(project, stack)
		rows = append(rows, []report.Cell{report.Text(project.Name), mark, result})
	}

	r.Table(report.Table{Headings: []string{"STACK", "", "CLOUD"}, Rows: rows})

	drifted, unread := 0, 0

	for _, project := range projects {
		switch {
		case project.Err != nil:
			unread++
		case len(project.Changes) > 0:
			drifted += len(project.Changes)

			r.Section(report.Warning, project.Name, describe(project.Changes))
		}
	}

	mark, line, hints := summary(drifted, unread)
	r.Summary(mark, line, hints...)

	if _, err := io.WriteString(out, r.String()); err != nil {
		return fmt.Errorf("write drift report: %w", err)
	}

	return nil
}

// verdict is a project's row: its mark and what the refresh preview said.
func verdict(project Project, stack string) (report.Cell, report.Cell) {
	switch {
	case project.Err != nil:
		return report.Failed, report.Cell{Text: project.Err.Error(), Color: report.Red}
	case !project.HasStack:
		return report.Nothing, report.Text("no " + stack + " stack")
	case len(project.Changes) == 0:
		return report.OK, report.Text("matches")
	default:
		return report.Warning, report.Text(report.Count(len(project.Changes), "resource differs", "resources differ"))
	}
}

// describe is a drifted project's section: one line per resource, the
// operation's sign first, as `pulumi preview` prints it.
func describe(changes []Change) []string {
	rows := make([][]string, 0, len(changes))

	for _, change := range changes {
		sign, what := "~", strings.Join(change.Fields, ", ")

		switch {
		case change.Op == OpDelete:
			sign, what = "-", "gone from the cloud"
		case what == "":
			what = "changed"
		}

		rows = append(rows, []string{sign + " " + change.Type, change.Name, what})
	}

	return report.Columns(rows, nil)
}

// summary is the closing line and what to do about it.
func summary(drifted, unread int) (report.Cell, string, []string) {
	switch {
	case unread > 0:
		return report.Failed, report.Count(unread, "stack", "stacks") + " could not be read, so drift is unknown there", nil
	case drifted > 0:
		return report.Warning, report.Count(drifted, "resource", "resources") + " changed outside Pulumi",
			[]string{"A refresh adopts the cloud's version into the state; an apply puts the code's back."}
	default:
		return report.OK, "every stack matches the cloud", nil
	}
}
