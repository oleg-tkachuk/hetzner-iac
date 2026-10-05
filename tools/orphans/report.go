package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/report"
)

// kindName is a kind as the report counts it: its row, and its plural.
type kindName struct{ kind, plural string }

// kindOrder is the order the report lists kinds in: what is billed first,
// then what only holds a name.
func kindOrder() []kindName {
	return []kindName{
		{KindServer, "servers"},
		{KindVolume, "volumes"},
		{KindLoadBalancer, "load balancers"},
		{KindPrimaryIP, "primary ips"},
		{KindFloatingIP, "floating ips"},
		{KindSnapshot, "snapshots"},
		{KindStorageBox, "storage boxes"},
		{KindSubaccount, "subaccounts"},
		{KindNetwork, "networks"},
		{KindFirewall, "firewalls"},
		{KindPlacementGroup, "placement groups"},
		{KindSSHKey, "ssh keys"},
		{KindCertificate, "certificates"},
		{KindZone, "dns zones"},
	}
}

// Header is what a report is about.
type Header struct {
	Stack   string
	Cluster string
	// ClusterGone is true when no server carries the cluster's label, so the
	// cluster claimed nothing.
	ClusterGone bool
	// Stacks is how many stack states were read.
	Stacks int
}

// Task is the task that prints the report, which its title names.
const Task = "cluster:orphans"

// The counts table's columns, and which of them are numeric.
const (
	columnFound     = 2
	columnUnclaimed = 3
)

// Counts is how many resources of each kind the inventory holds.
func (i Inventory) Counts() map[string]int {
	counts := map[string]int{
		KindVolume:       len(i.Volumes),
		KindLoadBalancer: len(i.LoadBalancers),
		KindServer:       len(i.Servers),
		KindPrimaryIP:    len(i.PrimaryIPs),
		KindSnapshot:     len(i.Snapshots),
	}

	for _, res := range i.Resources {
		counts[res.Kind]++
	}

	return counts
}

// Report renders what was read and what nothing claims.
//
// The counts come first, so a clean report is evidence rather than silence:
// "nothing unclaimed" over an empty project is what a wrong token looks like,
// and the table says which it is.
func Report(header Header, inventory Inventory, found []Finding, paint report.Painter) string {
	r := report.New(paint)
	counts := inventory.Counts()

	r.Title(Task, header.Stack)
	r.Facts(facts(header, counts))
	r.Table(countsTable(counts, found))

	byKind := map[string][]Finding{}
	for _, finding := range found {
		byKind[finding.Kind] = append(byKind[finding.Kind], finding)
	}

	for _, kind := range kindOrder() {
		if group := byKind[kind.kind]; len(group) > 0 {
			r.Section(report.Warning, kind.kind, findingLines(group))
		}
	}

	mark, verdict, hints := summary(header, counts, found)
	r.Summary(mark, verdict, hints...)

	return r.String()
}

// facts is what was read: the cluster, how much, and which kinds the project
// holds none of.
func facts(header Header, counts map[string]int) []report.Fact {
	cluster := header.Cluster
	if header.ClusterGone {
		// First, so the list below reads as an inventory of a teardown rather
		// than as a catastrophe.
		cluster += report.Separator + "gone: no server carries its label"
	}

	total, kinds := 0, 0

	var absent []string

	for _, kind := range kindOrder() {
		if counts[kind.kind] == 0 {
			absent = append(absent, kind.plural)

			continue
		}

		total += counts[kind.kind]
		kinds++
	}

	facts := []report.Fact{
		{Label: "cluster", Value: cluster},
		{Label: "checked", Value: report.Count(total, "resource", "resources") + " of " +
			report.Count(kinds, "kind", "kinds") + report.Separator +
			"against " + report.Count(header.Stacks, "stack state", "stack states")},
	}

	if len(absent) > 0 {
		facts = append(facts, report.Fact{Label: "absent", Value: strings.Join(absent, ", ")})
	}

	return facts
}

// countsTable is a row per kind the project holds: how many, and how many
// nothing claims.
func countsTable(counts map[string]int, found []Finding) report.Table {
	unclaimed := map[string]int{}
	for _, finding := range found {
		unclaimed[finding.Kind]++
	}

	table := report.Table{
		Headings: []string{"KIND", "", "FOUND", "UNCLAIMED"},
		Right:    map[int]bool{columnFound: true, columnUnclaimed: true},
	}

	for _, kind := range kindOrder() {
		if counts[kind.kind] == 0 {
			continue
		}

		mark, orphaned := report.OK, report.Text("")
		if n := unclaimed[kind.kind]; n > 0 {
			mark, orphaned = report.Warning, report.Cell{Text: strconv.Itoa(n), Color: report.Yellow}
		}

		table.Rows = append(table.Rows, []report.Cell{
			report.Text(kind.kind), mark, report.Text(strconv.Itoa(counts[kind.kind])), orphaned,
		})
	}

	return table
}

// findingLines is one kind's section: name, size when the kind has one, and
// why nothing claims it.
func findingLines(group []Finding) []string {
	sized := false
	for _, finding := range group {
		sized = sized || finding.Size > 0
	}

	rows := make([][]string, 0, len(group))

	for _, finding := range group {
		if sized {
			rows = append(rows, []string{finding.Name, formatSize(finding.Size), finding.Why})
		} else {
			rows = append(rows, []string{finding.Name, finding.Why})
		}
	}

	return report.Columns(rows, map[int]bool{1: sized})
}

// summary is the closing line and what to do before deleting anything.
func summary(header Header, counts map[string]int, found []Finding) (report.Cell, string, []string) {
	total := 0
	for _, n := range counts {
		total += n
	}

	if len(found) == 0 {
		return report.OK, fmt.Sprintf("all %d resources are claimed", total), nil
	}

	provisioned := 0

	for _, finding := range found {
		// Only volumes: an image's size is a compressed artefact, and adding
		// it to provisioned block storage would produce a total that means
		// nothing.
		if finding.Kind == KindVolume {
			provisioned += int(finding.Size)
		}
	}

	verb := "are"
	if len(found) == 1 {
		verb = "is"
	}

	verdict := fmt.Sprintf("%d of %s %s unclaimed", len(found), report.Count(total, "resource", "resources"), verb)
	if provisioned > 0 {
		verdict += report.Separator + strconv.Itoa(provisioned) + " GiB of provisioned volumes"
	}

	hints := []string{
		"Nothing was deleted. Most kinds are billed while they exist; read each reason first —",
		"a volume whose PersistentVolume is gone still holds its data.",
	}

	if header.ClusterGone {
		hints = append([]string{"The cluster is gone, so this is what the teardown left and no stack holds."}, hints...)
	}

	return report.Warning, verdict, hints
}

// formatSize keeps a compressed image size readable beside a volume's, and is
// empty for a kind with no size.
//
// A snapshot is a fraction of a gigabyte and a volume is fifty, so one format
// cannot serve both: %.0f prints "0 Gi" for the image, and %.1f prints
// "50.0 Gi" for the volume.
func formatSize(size float64) string {
	const wholeFrom = 10

	switch {
	case size <= 0:
		return ""
	case size < wholeFrom:
		return fmt.Sprintf("%.1f Gi", size)
	default:
		return fmt.Sprintf("%.0f Gi", size)
	}
}
