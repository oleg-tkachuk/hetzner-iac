// Package stackstatus renders the report behind `task platform:status`: every
// tier's and every layer's Pulumi stack for one environment, and whether they
// agree with each other.
//
// It holds no Pulumi import on purpose. tools/stackstatus reads the stacks
// through the Automation API and maps what it gets onto these types, so the
// judgements — which row needs attention, and why — are testable without a
// backend, and the package stays on the light side of the split
// internal/ci/packageweight_test.go holds.
package stackstatus

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/report"
)

// Result is the outcome of a stack's last operation, as Pulumi records it.
type Result string

// The results Pulumi records for an update.
const (
	ResultSucceeded  Result = "succeeded"
	ResultFailed     Result = "failed"
	ResultInProgress Result = "in-progress"
	ResultNotStarted Result = "not-started"
)

// Resource change kinds as Pulumi counts them in an update's resourceChanges.
const (
	ChangeCreate  = "create"
	ChangeUpdate  = "update"
	ChangeDelete  = "delete"
	ChangeReplace = "replace"
	ChangeSame    = "same"
)

const (
	// shortSHALength is how much of a commit hash the report prints.
	shortSHALength = 8

	// hoursPerDay turns an age in hours into days.
	hoursPerDay = 24

	// Clock formats for When: the time of day, with the date once it is not today.
	clockToday   = "15:04"
	clockEarlier = "Jan 02 15:04"

	// none stands in for a value the report does not have.
	none = report.None

	// resourcesColumn is the one right-aligned (numeric) column.
	resourcesColumn = 5

	// Task is the task that prints the report, which its title names.
	Task = "platform:status"
)

// changeSymbol pairs a change kind with the sign `pulumi preview` gives it.
type changeSymbol struct{ kind, symbol string }

// changeSymbols orders the change kinds a reader scans for first.
func changeSymbols() []changeSymbol {
	return []changeSymbol{
		{ChangeCreate, "+"},
		{ChangeUpdate, "~"},
		{ChangeReplace, "±"},
		{ChangeDelete, "-"},
		{ChangeSame, "="},
	}
}

// columns are the table's headings, in order.
func columns() []string {
	return []string{"STACK", "", "LAST RUN", "WHEN", "TOOK", "RESOURCES", "CHANGES", "COMMIT", "NOTES"}
}

// Commit is a git revision as Pulumi recorded it, or as the working tree has it.
type Commit struct {
	SHA    string
	Branch string
	Dirty  bool
}

// Short is the abbreviated hash, with a `*` when the tree was dirty.
func (c Commit) Short() string {
	if c.SHA == "" {
		return ""
	}

	sha := c.SHA
	if len(sha) > shortSHALength {
		sha = sha[:shortSHALength]
	}

	if c.Dirty {
		sha += "*"
	}

	return sha
}

// Update is the last operation on a stack.
type Update struct {
	Number  int // the stack's own update counter
	Kind    string
	Result  Result
	Start   time.Time
	End     time.Time // zero while it runs
	Changes map[string]int
	Commit  Commit
}

// State is what a stack's checkpoint says about operations that did not
// finish cleanly. Every field is a count, and every non-zero one is a reason
// the next update will not start from where the last one claims to have ended.
type State struct {
	// PendingOperations are operations the engine began and never recorded
	// the end of — an update that was killed. Pulumi refuses the next update
	// until they are resolved.
	PendingOperations int
	// PendingCreates are the URNs of the pending operations that were creates.
	// Each names a resource that may exist in the cloud or may not, and only
	// somebody looking can say which — see Recovery.
	PendingCreates []string
	// PendingDeletion are resources a create-before-delete replacement left
	// behind: the new one exists, the old one is still billed.
	PendingDeletion int
	// PendingReplacement are resources deleted ahead of their replacement,
	// which has not been created yet.
	PendingReplacement int
	// Tainted are resources marked for replacement on the next update.
	Tainted int
	// InitErrors are resources created but not initialised — a Helm release
	// or a server whose readiness check failed.
	InitErrors int
}

// Problems is one short phrase per non-zero count, in the order an operator
// would act on them.
func (s State) Problems() []string {
	var problems []string

	for _, p := range []struct {
		n     int
		label string
	}{
		{s.PendingOperations, "pending operation(s) — an update was interrupted"},
		{s.PendingDeletion, "awaiting deletion"},
		{s.PendingReplacement, "awaiting replacement"},
		{s.Tainted, "tainted"},
		{s.InitErrors, "with init errors"},
	} {
		if p.n > 0 {
			problems = append(problems, strconv.Itoa(p.n)+" "+p.label)
		}
	}

	return problems
}

// Project is one row of the report: a tier or a layer, and its stack.
type Project struct {
	Name string
	// Dir is the project's directory, as Pulumi's -C takes it.
	Dir string
	// IsCluster marks the cluster tier, which every other project references.
	IsCluster  bool
	HasStack   bool
	InProgress bool
	Resources  *int // nil when Pulumi reports no count
	Last       *Update
	State      State
	// ClusterRef is the project's clusterStackRef config; unused for the
	// cluster tier itself.
	ClusterRef string
	// Contract is the contractVersion output the stack last exported: the
	// version the cluster tier publishes, or the one a consumer was applied
	// against. Nil when the stack exports none.
	Contract *int
	Err      error // reading the stack failed; the row says so
}

// Cluster is what the cluster tier's stack says about the cluster.
type Cluster struct {
	Name     string
	Location string
	Endpoint string
	Contract *int
	// Stack is `<project>/<stack>`, the tail every clusterStackRef must end in.
	Stack string
	// Console is the stack's page in the Pulumi Cloud console.
	Console string
}

// Header is what the report says once, above the table.
type Header struct {
	Stack         string
	Backend       string
	User          string
	PulumiVersion string
	Head          Commit // the working tree's HEAD
	Cluster       Cluster
	// WantContract is the contract version this checkout's code reads.
	WantContract int
}

// Ago renders how long before now then was, in the largest whole unit.
func Ago(now, then time.Time) string {
	age := now.Sub(then)

	switch {
	case age < time.Minute:
		return fmt.Sprintf("%ds ago", int(age.Seconds()))
	case age < time.Hour:
		return fmt.Sprintf("%dm ago", int(age.Minutes()))
	case age < hoursPerDay*time.Hour:
		return fmt.Sprintf("%dh ago", int(age.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(age.Hours()/hoursPerDay))
	}
}

// When renders then as a clock time in now's zone, followed by how long ago it was.
func When(now, then time.Time) string {
	then = then.In(now.Location())

	layout := clockEarlier
	if year, month, day := then.Date(); year == now.Year() && month == now.Month() && day == now.Day() {
		layout = clockToday
	}

	return then.Format(layout) + " · " + Ago(now, then)
}

// Took renders an operation's duration, rounded to the second.
func Took(start, end time.Time) string {
	if end.IsZero() || end.Before(start) {
		return none
	}

	return end.Sub(start).Round(time.Second).String()
}

// ChangeSummary renders an update's resource changes, known kinds first in a
// fixed order, anything else after by name.
func ChangeSummary(changes map[string]int) string {
	if len(changes) == 0 {
		return none
	}

	known := map[string]bool{}
	parts := []string{}

	for _, cs := range changeSymbols() {
		known[cs.kind] = true

		if n := changes[cs.kind]; n > 0 {
			parts = append(parts, cs.symbol+strconv.Itoa(n))
		}
	}

	other := []string{}

	for kind, n := range changes {
		if !known[kind] && n > 0 {
			other = append(other, kind+" "+strconv.Itoa(n))
		}
	}

	sort.Strings(other)

	return strings.Join(append(parts, other...), " ")
}

// Notes are the reasons a row needs attention, beyond its last result: an
// interrupted checkpoint, a reference to another environment's cluster, a
// contract the two sides no longer agree on.
func Notes(project Project, header Header) []string {
	if project.Err != nil || !project.HasStack {
		return nil
	}

	notes := project.State.Problems()

	if project.IsCluster {
		if published := contractOf(project.Contract); published < header.WantContract {
			notes = append(notes, fmt.Sprintf(
				"publishes contract v%d, this checkout reads v%d — apply the cluster tier", published, header.WantContract))
		}

		return notes
	}

	switch {
	case project.ClusterRef == "":
		notes = append(notes, "clusterStackRef is not set")
	case header.Cluster.Stack != "" && !strings.HasSuffix(project.ClusterRef, "/"+header.Cluster.Stack):
		notes = append(notes, "references "+project.ClusterRef)
	}

	if project.Contract != nil && header.Cluster.Contract != nil && *project.Contract != *header.Cluster.Contract {
		notes = append(notes, fmt.Sprintf(
			"applied against contract v%d, the cluster publishes v%d", *project.Contract, *header.Cluster.Contract))
	}

	return notes
}

// Recovery is how to clear an interrupted update from a stack, as commands to
// run: nothing when there is nothing to clear.
//
// Pulumi refuses the next update while a pending operation remains. A pending
// update or delete clears on a refresh. A pending create does not, because
// the engine cannot know whether the cloud finished it: if the resource
// exists, clearing the create orphans it — billed, and in no state — so it is
// imported by its ID instead, and only one that does not exist is cleared.
// That is a judgement this report cannot make, so it names each URN and both
// commands rather than choosing.
func Recovery(project Project, stack string) []string {
	if project.Err != nil || project.State.PendingOperations == 0 {
		return nil
	}

	pulumi := fmt.Sprintf("pulumi -C %s --stack %s refresh", project.Dir, stack)

	lines := []string{fmt.Sprintf("recover %s — %d pending operation(s) from an interrupted update:",
		project.Name, project.State.PendingOperations)}

	if creates := len(project.State.PendingCreates); creates > 0 {
		lines = append(lines,
			fmt.Sprintf("  %d interrupted create(s); look each one up in the cloud before choosing:", creates))

		for _, urn := range project.State.PendingCreates {
			lines = append(lines, "    "+urn)
		}

		lines = append(lines,
			"  it exists  "+pulumi+" --import-pending-creates <urn> <id>",
			"  it does not "+pulumi+" --clear-pending-creates")
	}

	if others := project.State.PendingOperations - len(project.State.PendingCreates); others > 0 {
		lines = append(lines, fmt.Sprintf("  %d interrupted update(s) or delete(s) clear on: %s", others, pulumi))
	}

	return lines
}

// contractOf reads an absent contract as version zero, the way
// internal/pkg/clusterref does: a stack applied before versioning existed.
func contractOf(version *int) int {
	if version == nil {
		return 0
	}

	return *version
}

// Render writes the whole report. It builds the text first and writes once,
// so a failed write is one error rather than a torn table.
func Render(out io.Writer, header Header, projects []Project, now time.Time, paint report.Painter) error {
	r := report.New(paint)

	r.Title(Task, header.Stack)
	r.Facts(facts(header, r))

	rows := make([][]report.Cell, 0, len(projects))
	for _, project := range projects {
		rows = append(rows, row(project, header, now))
	}

	r.Table(report.Table{Headings: columns(), Right: map[int]bool{resourcesColumn: true}, Rows: rows})

	mark, verdict := summary(header, projects, r)
	r.Summary(mark, verdict)

	for _, project := range projects {
		r.Lines(Recovery(project, header.Stack))
	}

	if _, err := io.WriteString(out, r.String()); err != nil {
		return fmt.Errorf("write status report: %w", err)
	}

	return nil
}

// facts is the header: the cluster, where the stacks live, and what reads them.
func facts(header Header, r *report.Report) []report.Fact {
	head := header.Head.Short()
	if header.Head.Branch != "" {
		head += " (" + header.Head.Branch + ")"
	}

	facts := []report.Fact{
		{Label: "cluster", Value: clusterLine(header.Cluster)},
		{Label: "backend", Value: orDash(header.Backend) + " as " + orDash(header.User)},
	}

	if header.Cluster.Console != "" {
		facts = append(facts, report.Fact{Label: "console", Value: header.Cluster.Console})
	}

	return append(facts,
		report.Fact{Label: "pulumi", Value: orDash(header.PulumiVersion)},
		report.Fact{Label: "HEAD", Value: orDash(head)},
		report.Fact{Label: "contract", Value: contractLine(header, r)},
	)
}

func clusterLine(cluster Cluster) string {
	var parts []string

	for _, part := range []string{cluster.Name, cluster.Location, cluster.Endpoint} {
		if part != "" {
			parts = append(parts, part)
		}
	}

	if len(parts) == 0 {
		return none
	}

	return strings.Join(parts, " · ")
}

func contractLine(header Header, r *report.Report) string {
	if header.Cluster.Contract == nil {
		return fmt.Sprintf("%s · this checkout reads v%d", none, header.WantContract)
	}

	published := *header.Cluster.Contract
	line := fmt.Sprintf("v%d published · this checkout reads v%d", published, header.WantContract)

	if published < header.WantContract {
		return line + "  " + r.Paint(report.Yellow, report.MarkWarning+" behind")
	}

	return line + "  " + r.Paint(report.Green, report.MarkOK)
}

func row(project Project, header Header, now time.Time) []report.Cell {
	name := report.Text(project.Name)

	switch {
	case project.Err != nil:
		return []report.Cell{name, report.Failed, {Text: "unreadable: " + firstLine(project.Err.Error()), Color: report.Red}}
	case !project.HasStack:
		return []report.Cell{name, report.Nothing, report.Text("no stack")}
	}

	notes := report.Cell{Text: strings.Join(Notes(project, header), "; "), Color: report.Yellow}

	if project.Last == nil {
		return []report.Cell{
			name, report.Nothing, report.Text("never run"), report.Text(""), report.Text(""),
			report.Text(count(project.Resources)), report.Text(""), report.Text(""), notes,
		}
	}

	last := project.Last
	mark := resultMark(last.Result, project.InProgress)

	if mark == report.OK && notes.Text != "" {
		mark = report.Warning
	}

	commit := report.Text(last.Commit.Short())
	if last.Commit.Dirty || (last.Commit.SHA != "" && last.Commit.SHA != header.Head.SHA) {
		commit.Color = report.Yellow
	}

	return []report.Cell{
		name,
		mark,
		report.Text(runLabel(last)),
		report.Text(When(now, last.Start)),
		report.Text(Took(last.Start, last.End)),
		report.Text(count(project.Resources)),
		report.Text(ChangeSummary(last.Changes)),
		commit,
		notes,
	}
}

// runLabel is the operation and the stack's update number, `update #42`.
func runLabel(last *Update) string {
	if last.Number <= 0 {
		return last.Kind
	}

	return last.Kind + " #" + strconv.Itoa(last.Number)
}

func resultMark(result Result, inProgress bool) report.Cell {
	switch {
	case inProgress || result == ResultInProgress:
		return report.Running
	case result == ResultSucceeded:
		return report.OK
	case result == ResultFailed:
		return report.Failed
	default:
		return report.Warning
	}
}

func count(n *int) string {
	if n == nil {
		return none
	}

	return strconv.Itoa(*n)
}

// tally counts the projects by outcome.
type tally struct {
	succeeded, failed, running, missing, attention, resources int
	commits                                                   map[string]bool
}

func countProjects(header Header, projects []Project) tally {
	counts := tally{commits: map[string]bool{}}

	for _, project := range projects {
		if project.Resources != nil {
			counts.resources += *project.Resources
		}

		if len(Notes(project, header)) > 0 {
			counts.attention++
		}

		switch {
		case project.Err != nil || !project.HasStack || project.Last == nil:
			counts.missing++
		case project.InProgress || project.Last.Result == ResultInProgress:
			counts.running++
		case project.Last.Result == ResultSucceeded:
			counts.succeeded++
		default:
			counts.failed++
		}

		if project.Last != nil && project.Last.Commit.SHA != "" {
			counts.commits[project.Last.Commit.SHA] = true
		}
	}

	return counts
}

// summary is the closing line: its mark is the worst row's, and the counts
// say why.
func summary(header Header, projects []Project, r *report.Report) (report.Cell, string) {
	counts := countProjects(header, projects)

	parts := []string{
		strconv.Itoa(len(projects)) + " stacks",
		r.Paint(report.Green, strconv.Itoa(counts.succeeded)+" succeeded"),
	}

	for _, extra := range []struct {
		n     int
		label string
		color string
	}{
		{counts.failed, " failed", report.Red},
		{counts.running, " running", report.Cyan},
		{counts.missing, " without a run", report.Yellow},
		{counts.attention, " need attention", report.Yellow},
	} {
		if extra.n > 0 {
			parts = append(parts, r.Paint(extra.color, strconv.Itoa(extra.n)+extra.label))
		}
	}

	parts = append(parts, strconv.Itoa(counts.resources)+" resources")

	switch {
	case len(counts.commits) == 1 && counts.commits[header.Head.SHA]:
		parts = append(parts, "all applied from HEAD")
	case len(counts.commits) > 0:
		parts = append(parts, r.Paint(report.Yellow, fmt.Sprintf("applied from %d commit(s), HEAD is %s",
			len(counts.commits), orDash(header.Head.Short()))))
	}

	mark := report.OK

	switch {
	case counts.failed > 0:
		mark = report.Failed
	case counts.missing > 0 || counts.attention > 0:
		mark = report.Warning
	case counts.running > 0:
		mark = report.Running
	}

	return mark, strings.Join(parts, report.Separator)
}

// firstLine keeps a multi-line CLI error to the sentence that names it.
func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")

	return line
}

func orDash(s string) string {
	if s == "" {
		return none
	}

	return s
}
