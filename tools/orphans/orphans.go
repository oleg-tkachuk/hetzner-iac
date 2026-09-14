package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/hetzner"
)

// The Hetzner resources this platform creates indirectly, and can therefore
// leave behind. Spelled once, because the report groups by them and a typo
// would print a heading nobody recognises.
const (
	KindVolume       = "volume"
	KindLoadBalancer = "load balancer"
	KindServer       = "server"
	KindPrimaryIP    = "primary ip"
	KindSnapshot     = "snapshot"
)

// ServiceUIDLabel is how the hcloud cloud controller manager records which
// Service a load balancer belongs to. A load balancer without it was not
// created from this cluster.
const ServiceUIDLabel = "hcloud-ccm/service-uid"

// TalosVersionLabel is the label cluster:image-bake stamps on the snapshot it
// bakes, and lookupTalosImage selects on. Aliased from pkg/hetzner rather than
// spelled again: this report groups snapshots by it, so a copy that drifted
// would file every snapshot under an empty version.
const TalosVersionLabel = hetzner.LabelTalosVersion

// Inventory is what the Hetzner project holds.
type Inventory struct {
	Volumes       []Volume
	LoadBalancers []LoadBalancer
	Servers       []Server
	PrimaryIPs    []PrimaryIP
	Snapshots     []Snapshot
}

// The shapes below are this check's own, filled from the Hetzner SDK's types
// at the edge. Deliberately not the SDK's structs: what the decision needs is
// a name, a size and one label, and a classifier that took the library's types
// would need the library to test.

// Volume is a block volume, whose name the CSI driver sets to the
// PersistentVolume's name.
type Volume struct {
	Name   string
	SizeGB int
}

// LoadBalancer is one load balancer and the labels the CCM put on it.
type LoadBalancer struct {
	Name   string
	Labels map[string]string
}

// Server is one server. Its type is reported because "which type" is the
// first thing asked about a server nobody expected.
type Server struct {
	Name string
	Type string
	// Labels, for the cluster label alone. It decides whether this check may
	// trust an empty set of claims — see ClusterServers.
	Labels map[string]string
}

// PrimaryIP is a reservable public address, which is billed while it exists
// whether or not anything is using it.
type PrimaryIP struct {
	Name       string
	IP         string
	AssigneeID int64
}

// Snapshot is a bootable image this repository baked. Its size is the
// compressed image in GB, not a provisioned volume.
type Snapshot struct {
	Description string
	SizeGB      float64
	Labels      map[string]string
}

// Claims is what the cluster says it is using, plus what the repository
// pinned. Everything in the inventory that no claim accounts for is reported.
type Claims struct {
	// PersistentVolumes are PV names, which equal the hcloud volume names the
	// CSI driver creates.
	PersistentVolumes map[string]bool
	// ServiceUIDs are the UIDs of every Service in the cluster. The CCM
	// records the owning Service's UID on the load balancer it creates.
	ServiceUIDs map[string]bool
	// Nodes are the Kubernetes node names, which are the server hostnames.
	Nodes map[string]bool
	// TalosVersion is the version the topology pins. A snapshot for another
	// version is not wrong, only unused — and still billed.
	TalosVersion string
}

// ClusterServers are the servers labelled as belonging to one cluster.
//
// This is the question the check could not previously answer, and everything
// else here depended on it. `Claims` comes from the cluster over kubectl, and
// an unreachable cluster returns nothing — which is indistinguishable from a
// cluster that is genuinely empty, so the check refused to run rather than
// call every volume in the project an orphan. Correct, and it made the tool
// useless at the one moment it is wanted: straight after `task destroy`,
// when the question is precisely "what did that leave behind".
//
// The label answers it without a cluster and without Pulumi state. No server
// carries this cluster's label, so no cluster exists, so an empty set of
// claims is not a failure to ask — it is the truth, and every remaining
// resource really is orphaned.
//
// Labelled rather than counted across the project: a shared Hetzner project
// can hold another cluster's servers, and those must not make this one look
// alive.
func ClusterServers(inventory Inventory, cluster string) []Server {
	var mine []Server

	for _, server := range inventory.Servers {
		if server.Labels[hetzner.LabelCluster] == cluster {
			mine = append(mine, server)
		}
	}

	return mine
}

// Finding is one resource nothing accounts for.
type Finding struct {
	Kind string
	Name string
	// Size in GB, zero when the kind has no size.
	Size float64
	// Why is what makes it an orphan, in the operator's terms.
	Why string
}

// Orphans is the whole judgement, as a pure function of two lists.
//
// Pure on purpose: the hard part here is not talking to an API, it is deciding
// what "nothing claims this" means per kind, and that decision is what a
// wrong answer would be expensive in — a volume called an orphan and deleted
// is data gone, and a real orphan called fine is a bill nobody reads.
func Orphans(inventory Inventory, claims Claims) []Finding {
	var found []Finding

	for _, volume := range inventory.Volumes {
		// The name, not the attachment. A volume detaches for a moment
		// whenever its pod is rescheduled, so "no server" alone would report
		// every rolling update. What makes it an orphan is that no
		// PersistentVolume of that name exists to claim it.
		if claims.PersistentVolumes[volume.Name] {
			continue
		}

		found = append(found, Finding{
			Kind: KindVolume, Name: volume.Name, Size: float64(volume.SizeGB),
			Why: "no PersistentVolume of this name in the cluster",
		})
	}

	for _, balancer := range inventory.LoadBalancers {
		uid, managed := balancer.Labels[ServiceUIDLabel]

		switch {
		case !managed:
			// Not created from this cluster. Reported rather than skipped,
			// because the operator is reading this list to find out what is
			// being paid for, and "something else made it" is an answer.
			found = append(found, Finding{
				Kind: KindLoadBalancer, Name: balancer.Name,
				Why: "no " + ServiceUIDLabel + " label: not created by this cluster's CCM",
			})
		case !claims.ServiceUIDs[uid]:
			found = append(found, Finding{
				Kind: KindLoadBalancer, Name: balancer.Name,
				Why: "its Service is gone (uid " + uid + ")",
			})
		}
	}

	for _, server := range inventory.Servers {
		if claims.Nodes[server.Name] {
			continue
		}

		// A server that is not a node is the shape a failed image bake
		// leaves: hcloud-upload-image boots one into rescue mode, and a crash
		// part way through leaves it running and billed.
		found = append(found, Finding{
			Kind: KindServer, Name: server.Name,
			Why: "not a node in this cluster (type " + server.Type + ")",
		})
	}

	for _, address := range inventory.PrimaryIPs {
		if address.AssigneeID != 0 {
			continue
		}

		found = append(found, Finding{
			Kind: KindPrimaryIP, Name: address.Name,
			Why: "unassigned (" + address.IP + ")",
		})
	}

	for _, snapshot := range inventory.Snapshots {
		version := snapshot.Labels[TalosVersionLabel]

		// Only Talos snapshots this repository bakes, and only the ones for
		// another version. The pinned one is what every server boots from.
		if version == "" || version == claims.TalosVersion {
			continue
		}

		found = append(found, Finding{
			Kind: KindSnapshot, Name: snapshot.Description, Size: snapshot.SizeGB,
			Why: "baked for Talos " + version + ", and the topology pins " + claims.TalosVersion,
		})
	}

	sort.SliceStable(found, func(i, j int) bool {
		if found[i].Kind != found[j].Kind {
			return found[i].Kind < found[j].Kind
		}

		return found[i].Name < found[j].Name
	})

	return found
}

// Examined is what the inventory held, so a clean report is evidence rather
// than silence.
//
// Without it "no orphaned resources" reads identically whether the project
// holds forty volumes that are all claimed or none at all — and the second is
// what a wrong token or an empty project looks like. The same distinction the
// cluster side already makes by failing instead of returning an empty list.
func (i Inventory) Examined() string {
	return fmt.Sprintf("%d volumes, %d load balancers, %d servers, %d addresses, %d snapshots",
		len(i.Volumes), len(i.LoadBalancers), len(i.Servers), len(i.PrimaryIPs), len(i.Snapshots))
}

// ClusterGoneNote is the header printed when the judgement was made without a
// cluster.
//
// Without it the report is alarming and unexplained: every volume and every
// load balancer is listed as claimed by nothing, which is correct and reads
// like a catastrophe. Saying why first turns the same list into an inventory
// of what a teardown left behind.
const ClusterGoneNote = "no server carries this cluster's label, so the cluster is gone and " +
	"nothing can claim anything.\nEverything below is what the teardown left behind."

// Report renders the findings against what was examined.
//
// note is printed before the table when there is one to print — see
// ClusterGoneNote.
func Report(found []Finding, examined, note string) string {
	header := ""
	if note != "" {
		header = note + "\n\n"
	}

	if len(found) == 0 {
		return header + "examined " + examined + "\nno orphans: every one of them is claimed\n"
	}

	var (
		out         strings.Builder
		provisioned int
	)

	out.WriteString(header)

	fmt.Fprintf(&out, "%-14s %-46s %9s  %s\n", "KIND", "NAME", "SIZE", "WHY")

	for _, finding := range found {
		size := ""

		if finding.Size > 0 {
			size = formatSize(finding.Size)
		}

		// Only volumes: an image's size is a compressed artefact, and adding
		// it to provisioned block storage would produce a total that means
		// nothing.
		if finding.Kind == KindVolume {
			provisioned += int(finding.Size)
		}

		fmt.Fprintf(&out, "%-14s %-46s %9s  %s\n", finding.Kind, finding.Name, size, finding.Why)
	}

	fmt.Fprintf(&out, "\nexamined %s\n%d of them nothing claims", examined, len(found))

	if provisioned > 0 {
		fmt.Fprintf(&out, ", including %d GiB of provisioned volumes", provisioned)
	}

	out.WriteString(".\nEach is still billed. Nothing here is deleted by this check — read the\n")
	out.WriteString("WHY column first: a volume whose PersistentVolume is gone still holds the\n")
	out.WriteString("data that was on it.\n")

	return out.String()
}

// formatSize keeps a compressed image size readable beside a volume's.
//
// A snapshot is a fraction of a gigabyte and a volume is fifty, so one format
// cannot serve both: %.0f prints "0 Gi" for the image, and %.1f prints
// "50.0 Gi" for the volume.
func formatSize(size float64) string {
	if size < 10 {
		return fmt.Sprintf("%.1f Gi", size)
	}

	return fmt.Sprintf("%.0f Gi", size)
}
