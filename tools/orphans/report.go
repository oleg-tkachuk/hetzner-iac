package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/stackstatus"
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

// The report's layout: the left margin, the gap between columns, and the
// headings of the counts table.
const (
	indent       = "  "
	gap          = "  "
	headKind     = "KIND"
	headFound    = "FOUND"
	headOrphaned = "UNCLAIMED"
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
func Report(header Header, inventory Inventory, found []Finding, paint stackstatus.Painter) string {
	var out strings.Builder

	fmt.Fprintf(&out, "%s %s · cluster %s · %d stack state(s) read\n\n",
		paint(stackstatus.Cyan, stackstatus.MarkRunning),
		paint(stackstatus.Bold, "orphans · stack "+header.Stack), header.Cluster, header.Stacks)

	// Before anything else: without it, a list of everything as unclaimed
	// reads like a catastrophe rather than an inventory of a teardown.
	if header.ClusterGone {
		fmt.Fprintf(&out, "%s%s no server carries %s's label: the cluster is gone, so everything\n"+
			"%s  below is what the teardown left behind and no stack holds.\n\n",
			indent, paint(stackstatus.Grey, stackstatus.MarkNone), header.Cluster, indent)
	}

	counts := inventory.Counts()
	writeCounts(&out, counts, found, paint)
	writeFindings(&out, found, paint)
	writeSummary(&out, counts, found, paint)

	return out.String()
}

// writeCounts is the table of kinds found, and the line naming the kinds the
// project holds none of.
func writeCounts(out *strings.Builder, counts map[string]int, found []Finding, paint stackstatus.Painter) {
	unclaimed := map[string]int{}
	for _, finding := range found {
		unclaimed[finding.Kind]++
	}

	width := len(headKind)

	var empty []string

	for _, kind := range kindOrder() {
		if counts[kind.kind] == 0 {
			empty = append(empty, kind.plural)

			continue
		}

		width = max(width, len(kind.kind))
	}

	fmt.Fprintf(out, "%s%s\n", indent, paint(stackstatus.Bold,
		fmt.Sprintf("%-*s%s%s%s%s", width, headKind, gap, headFound, gap, headOrphaned)))

	for _, kind := range kindOrder() {
		if counts[kind.kind] == 0 {
			continue
		}

		row := fmt.Sprintf("%-*s%s%*d", width, kind.kind, gap, len(headFound), counts[kind.kind])
		if n := unclaimed[kind.kind]; n > 0 {
			row += gap + paint(stackstatus.Yellow, fmt.Sprintf("%*d", len(headOrphaned), n))
		}

		fmt.Fprintf(out, "%s%s\n", indent, row)
	}

	if len(empty) > 0 {
		fmt.Fprintf(out, "%s%s none of: %s\n", indent, paint(stackstatus.Grey, stackstatus.MarkNone),
			strings.Join(empty, ", "))
	}

	out.WriteString("\n")
}

// writeFindings lists what nothing claims, one group per kind.
func writeFindings(out *strings.Builder, found []Finding, paint stackstatus.Painter) {
	byKind := map[string][]Finding{}
	for _, finding := range found {
		byKind[finding.Kind] = append(byKind[finding.Kind], finding)
	}

	for _, kind := range kindOrder() {
		group := byKind[kind.kind]
		if len(group) == 0 {
			continue
		}

		fmt.Fprintf(out, "%s%s %s\n", indent, paint(stackstatus.Yellow, stackstatus.MarkWarning),
			paint(stackstatus.Bold, kind.kind))

		nameWidth, sizeWidth := 0, 0
		for _, finding := range group {
			nameWidth = max(nameWidth, len(finding.Name))
			sizeWidth = max(sizeWidth, len(formatSize(finding.Size)))
		}

		for _, finding := range group {
			row := fmt.Sprintf("%-*s", nameWidth, finding.Name)
			if sizeWidth > 0 {
				row += gap + fmt.Sprintf("%*s", sizeWidth, formatSize(finding.Size))
			}

			fmt.Fprintf(out, "%s%s%s%s%s\n", indent, indent, row, gap, finding.Why)
		}

		out.WriteString("\n")
	}
}

// writeSummary is the last line: whether anything is unclaimed, and what to
// do before deleting it.
func writeSummary(out *strings.Builder, counts map[string]int, found []Finding, paint stackstatus.Painter) {
	total := 0
	for _, n := range counts {
		total += n
	}

	if len(found) == 0 {
		fmt.Fprintf(out, "%s%s all %d resources are claimed\n", indent,
			paint(stackstatus.Green, stackstatus.MarkOK), total)

		return
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

	line := fmt.Sprintf("%d of %d resources %s unclaimed", len(found), total, verb)
	if provisioned > 0 {
		line += " · " + strconv.Itoa(provisioned) + " GiB of provisioned volumes"
	}

	fmt.Fprintf(out, "%s%s %s\n", indent, paint(stackstatus.Yellow, stackstatus.MarkWarning), paint(stackstatus.Bold, line))
	fmt.Fprintf(out, "%s  Nothing was deleted. Most kinds are billed while they exist; read each\n", indent)
	fmt.Fprintf(out, "%s  reason first — a volume whose PersistentVolume is gone still holds its data.\n", indent)
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
