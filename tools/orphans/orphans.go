package main

import (
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
)

// The Hetzner resources this check lists: every kind the API holds per
// project that a stack, the cluster or an operator can create and then forget.
// Spelled once, because the report groups by them and a typo would print a
// heading nobody recognises.
//
// Not listed: server backups, which the API deletes with their server; storage
// box snapshots, which live and die with their box; and the catalogues —
// system images, ISOs, types, locations — which nobody in the project owns.
const (
	KindVolume         = "volume"
	KindLoadBalancer   = "load balancer"
	KindServer         = "server"
	KindPrimaryIP      = "primary ip"
	KindFloatingIP     = "floating ip"
	KindSnapshot       = "snapshot"
	KindNetwork        = "network"
	KindFirewall       = "firewall"
	KindPlacementGroup = "placement group"
	KindSSHKey         = "ssh key"
	KindCertificate    = "certificate"
	KindStorageBox     = "storage box"
	KindSubaccount     = "subaccount"
	KindZone           = "dns zone"
)

// ServiceUIDLabel is how the hcloud cloud controller manager records which
// Service a load balancer belongs to.
//
// Its absence does not mean "not this cluster's": both load balancers here are
// made through the Hetzner provider, because the CCM refuses to target a node
// carrying node.kubernetes.io/exclude-from-external-load-balancers, which
// Talos puts on every control-plane node. Those are claimed by the stack that
// holds them.
const ServiceUIDLabel = "hcloud-ccm/service-uid"

// TalosVersionLabel is the label cluster:image:bake stamps on the snapshot it
// bakes, and lookupTalosImage selects on. Aliased from internal/pkg/clusterspec rather than
// spelled again: this report groups snapshots by it, so a copy that drifted
// would file every snapshot under an empty version.
const TalosVersionLabel = clusterspec.LabelTalosVersion

// Inventory is what the Hetzner project holds.
type Inventory struct {
	Volumes       []Volume
	LoadBalancers []LoadBalancer
	Servers       []Server
	PrimaryIPs    []PrimaryIP
	Snapshots     []Snapshot
	// Resources are every other kind. Nothing in the cluster can claim them,
	// so a stack's state is the only claimant.
	Resources []Resource
}

// The shapes below are this check's own, filled from the Hetzner SDK's types
// at the edge. Deliberately not the SDK's structs: what the decision needs is
// a name, a size and one label, and a classifier that took the library's types
// would need the library to test.

// Every shape carries the API's ID, which is the ID a stack's state records
// for the same resource.

// Volume is a block volume, whose name the CSI driver sets to the
// PersistentVolume's name.
type Volume struct {
	ID     int64
	Name   string
	SizeGB int
}

// LoadBalancer is one load balancer and its labels.
type LoadBalancer struct {
	ID     int64
	Name   string
	Labels map[string]string
}

// Server is one server. Its type is reported because "which type" is the
// first thing asked about a server nobody expected.
type Server struct {
	ID   int64
	Name string
	Type string
	// Labels, for the cluster label alone. It decides whether this check may
	// trust an empty set of claims — see ClusterServers.
	Labels map[string]string
}

// PrimaryIP is a reservable public address, which is billed while it exists
// whether or not anything is using it.
//
// One with no labels and an assignee is usually not anybody's to claim: the API
// creates a load balancer's addresses itself and assigns them to it.
type PrimaryIP struct {
	ID         int64
	Name       string
	IP         string
	AssigneeID int64
}

// Snapshot is a bootable image this repository baked. Its size is the
// compressed image in GB, not a provisioned volume.
type Snapshot struct {
	ID          int64
	Description string
	SizeGB      float64
	Labels      map[string]string
}

// Resource is one resource of a kind only a stack can claim.
type Resource struct {
	Kind   string
	ID     int64
	Name   string
	Labels map[string]string
}

// Held is what the stacks' states hold: per kind, the API IDs.
type Held struct {
	IDs map[string]map[int64]bool
	// Zones are the DNS zones a stack writes record sets into, as the record
	// set names them: by name or by ID, both of which the provider accepts.
	// The zone is made by hand and delegated; the records are what claim it.
	Zones map[string]bool
	// Stacks is how many states were read, so a report can say so.
	Stacks int
}

// NewHeld is an empty Held, ready to add to.
func NewHeld() Held {
	return Held{IDs: map[string]map[int64]bool{}, Zones: map[string]bool{}}
}

// Add records that a stack holds the resource.
func (h Held) Add(kind string, id int64) {
	if h.IDs[kind] == nil {
		h.IDs[kind] = map[int64]bool{}
	}

	h.IDs[kind][id] = true
}

// Holds reports whether any stack holds the resource.
func (h Held) Holds(kind string, id int64) bool {
	return h.IDs[kind][id]
}

// HoldsZone reports whether a stack writes record sets into the zone.
func (h Held) HoldsZone(zone Resource) bool {
	return h.Zones[zone.Name] || h.Zones[strconv.FormatInt(zone.ID, 10)]
}

// RefuseEmptyBackend refuses a judgement against no stacks while the
// cluster's servers exist: the backend is not the one that made them, and
// every resource would read as unheld. With no servers, no stacks is the truth
// after a teardown.
func RefuseEmptyBackend(servers, stacks int) error {
	if servers > 0 && stacks == 0 {
		return fmt.Errorf("%w: %d server(s) carry this cluster's label", ErrNoStacks, servers)
	}

	return nil
}

// ErrNoStacks is RefuseEmptyBackend's refusal.
var ErrNoStacks = errors.New("no project directory has a stack, yet the cluster's servers exist: " +
	"is this the backend that made them?")

// Claims is what the cluster says it is using, what the stacks hold, and what
// the repository pinned. Everything in the inventory that no claim accounts
// for is reported.
type Claims struct {
	// Held is what the stacks' states hold. A held resource is the stack's to
	// destroy, whoever else uses it.
	Held Held
	// PersistentVolumes are PV names, which equal the hcloud volume names the
	// CSI driver creates.
	PersistentVolumes map[string]bool
	// ReleasedVolumes are the PV names whose phase is Released: the claim is
	// gone and the volume was kept, which is what `reclaimPolicy: Retain`
	// exists to do. A subset of PersistentVolumes, and the reason this is a
	// second set rather than a flag is that the first answers "does anything
	// claim this" and this one answers "is anything ever going to use it".
	ReleasedVolumes map[string]bool
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
		if server.Labels[clusterspec.LabelCluster] == cluster {
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

// PhaseReleased is the PersistentVolume phase that means the claim is gone and
// the volume was kept. Spelled here rather than imported from k8s.io/api: this
// program asks the cluster through kubectl and links no Kubernetes client.
const PhaseReleased = "Released"

// Orphans is the whole judgement, as a pure function of two lists.
//
// Pure on purpose: the hard part here is not talking to an API, it is deciding
// what "nothing claims this" means per kind, and that decision is what a
// wrong answer would be expensive in — a volume called an orphan and deleted
// is data gone, and a real orphan called fine is a bill nobody reads.
func Orphans(inventory Inventory, claims Claims) []Finding {
	var found []Finding

	found = append(found, volumeFindings(inventory.Volumes, claims)...)

	found = append(found, loadBalancerFindings(inventory.LoadBalancers, claims)...)

	for _, server := range inventory.Servers {
		if claims.Nodes[server.Name] || claims.Held.Holds(KindServer, server.ID) {
			continue
		}

		// A server that is not a node is the shape a failed image bake
		// leaves: hcloud-upload-image boots one into rescue mode, and a crash
		// part way through leaves it running and billed.
		found = append(found, Finding{
			Kind: KindServer, Name: server.Name,
			Why: "not a node in this cluster, and no stack holds it (type " + server.Type + ")",
		})
	}

	for _, address := range inventory.PrimaryIPs {
		if address.AssigneeID != 0 || claims.Held.Holds(KindPrimaryIP, address.ID) {
			continue
		}

		found = append(found, Finding{
			Kind: KindPrimaryIP, Name: address.Name,
			Why: "unassigned, and no stack holds it (" + address.IP + ")",
		})
	}

	found = append(found, snapshotFindings(inventory.Snapshots, claims)...)

	for _, res := range inventory.Resources {
		if claims.Held.Holds(res.Kind, res.ID) || (res.Kind == KindZone && claims.Held.HoldsZone(res)) {
			continue
		}

		found = append(found, Finding{Kind: res.Kind, Name: res.Name, Why: unheld(res.Labels)})
	}

	sort.SliceStable(found, func(i, j int) bool {
		if found[i].Kind != found[j].Kind {
			return found[i].Kind < found[j].Kind
		}

		return found[i].Name < found[j].Name
	})

	return found
}

// snapshotFindings is the snapshot half of Orphans: only Talos snapshots this
// repository bakes, and only the ones for another version. The pinned one is
// what every server boots from.
func snapshotFindings(snapshots []Snapshot, claims Claims) []Finding {
	var found []Finding

	for _, snapshot := range snapshots {
		version := snapshot.Labels[TalosVersionLabel]

		if claims.Held.Holds(KindSnapshot, snapshot.ID) || version == "" || version == claims.TalosVersion {
			continue
		}

		found = append(found, Finding{
			Kind: KindSnapshot, Name: snapshot.Description, Size: snapshot.SizeGB,
			Why: "baked for Talos " + version + ", and the topology pins " + claims.TalosVersion,
		})
	}

	return found
}

// unheld says why a resource no stack holds is an orphan, from who made it.
//
// One this repository labelled is what a destroy that stopped part-way, or a
// stack removed with --force, leaves. One it did not label was made by hand or
// by another tool, and is reported rather than skipped: the operator reads
// this list to find out what is being paid for, and "something else made it"
// is an answer.
func unheld(labels map[string]string) string {
	if labels[clusterspec.LabelManagedBy] == clusterspec.ManagedBy {
		return "made by this repository for " + labels[clusterspec.LabelCluster] +
			", and no stack holds it: a destroy stopped part-way, or a stack was removed"
	}

	return "made by neither this repository nor the cluster, and no stack holds it"
}

// volumeFindings is the volume half of Orphans, split out because the two
// questions a volume raises are different: whether anything claims it, and
// whether anything will ever use it again.
func volumeFindings(volumes []Volume, claims Claims) []Finding {
	var found []Finding

	for _, volume := range volumes {
		// One a stack made is the stack's, PersistentVolume or not.
		if claims.Held.Holds(KindVolume, volume.ID) {
			continue
		}

		// A Released volume is claimed by a PersistentVolume and used by
		// nothing: the PVC is gone and Kubernetes will not bind that PV to a
		// new claim by itself. So the object exists, the bill continues, and
		// the check that only asks "does a PersistentVolume of this name
		// exist" says it is fine.
		//
		// The message names no reclaim policy, because two states reach here
		// and both were seen in one live run: on the retaining class this is
		// the intended outcome of a deleted claim, and on the deleting class it
		// means the driver has not removed the volume yet — or cannot.
		//
		// Reported, not called deleteable. Retention is the point of the class
		// a database's volume is on, and the operator reading this list is the
		// one who knows whether the data is still wanted.
		if claims.ReleasedVolumes[volume.Name] {
			found = append(found, Finding{
				Kind: KindVolume, Name: volume.Name, Size: float64(volume.SizeGB),
				Why: "its PersistentVolume is " + PhaseReleased + ": the claim is gone, so " +
					"nothing will bind it again until somebody says so",
			})

			continue
		}

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

	return found
}

// loadBalancerFindings is the load-balancer half of Orphans, split out because
// who made a balancer decides which claim can account for it: a Service for
// the CCM's, a stack for this repository's own.
func loadBalancerFindings(balancers []LoadBalancer, claims Claims) []Finding {
	var found []Finding

	for _, balancer := range balancers {
		if claims.Held.Holds(KindLoadBalancer, balancer.ID) {
			continue
		}

		uid, fromCCM := balancer.Labels[ServiceUIDLabel]

		switch {
		case !fromCCM:
			found = append(found, Finding{Kind: KindLoadBalancer, Name: balancer.Name, Why: unheld(balancer.Labels)})
		case !claims.ServiceUIDs[uid]:
			found = append(found, Finding{
				Kind: KindLoadBalancer, Name: balancer.Name,
				Why: "its Service is gone (uid " + uid + ")",
			})
		}
	}

	return found
}
