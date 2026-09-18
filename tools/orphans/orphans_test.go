package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
)

// claims is a cluster that accounts for everything named in it.
func claims() Claims {
	return Claims{
		PersistentVolumes: map[string]bool{"pvc-kept": true},
		ServiceUIDs:       map[string]bool{"uid-kept": true},
		Nodes:             map[string]bool{"node-kept": true},
		TalosVersion:      "v1.13.10",
	}
}

func TestOrphans_SaysNothingWhenEverythingIsClaimed(t *testing.T) {
	t.Parallel()

	// The case that must never produce a finding, because a false positive
	// here invites an operator to delete a volume that is in use.
	found := Orphans(Inventory{
		Volumes:       []Volume{{Name: "pvc-kept", SizeGB: 50}},
		LoadBalancers: []LoadBalancer{{Name: "lb", Labels: map[string]string{ServiceUIDLabel: "uid-kept"}}},
		Servers:       []Server{{Name: "node-kept", Type: "cx23"}},
		PrimaryIPs:    []PrimaryIP{{Name: "ip", IP: "192.0.2.1", AssigneeID: 42}},
		Snapshots: []Snapshot{{
			Description: "talos", Labels: map[string]string{TalosVersionLabel: "v1.13.10"},
		}},
	}, claims())

	assert.Empty(t, found)
}

func TestOrphans_AVolumeIsJudgedByItsNameNotItsAttachment(t *testing.T) {
	t.Parallel()

	// A volume detaches for a moment whenever its pod is rescheduled, so
	// "no server" would report every rolling update. What makes it an orphan
	// is that no PersistentVolume of that name exists — which is the state a
	// destroyed cluster leaves, because the API server that would have told
	// the CSI driver to delete it went with the cluster.
	found := Orphans(Inventory{Volumes: []Volume{
		{Name: "pvc-kept", SizeGB: 50},
		{Name: "pvc-gone", SizeGB: 20},
	}}, claims())

	require.Len(t, found, 1)
	assert.Equal(t, KindVolume, found[0].Kind)
	assert.Equal(t, "pvc-gone", found[0].Name)
	assert.InDelta(t, 20, found[0].Size, 0)
	assert.Contains(t, found[0].Why, "no PersistentVolume")
}

func TestOrphans_LoadBalancers(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		labels map[string]string
		why    string
	}{
		// The shape a deleted Service leaves behind: the CCM removes the load
		// balancer when the Service goes, but not when the whole cluster does.
		"service gone": {
			labels: map[string]string{ServiceUIDLabel: "uid-gone"},
			why:    "its Service is gone",
		},
		// Reported rather than skipped: the operator is reading this to find
		// out what is being paid for, and "something else made it" is an
		// answer, not a reason to stay silent.
		"not ours": {
			labels: map[string]string{"team": "platform"},
			why:    "created neither by this cluster's CCM nor by this repository",
		},
	} {
		found := Orphans(Inventory{
			LoadBalancers: []LoadBalancer{{Name: "lb-" + name, Labels: tc.labels}},
		}, claims())

		require.Len(t, found, 1, name)
		assert.Equal(t, KindLoadBalancer, found[0].Kind, name)
		assert.Contains(t, found[0].Why, tc.why, name)
	}
}

// TestOrphans_PulumiLoadBalancersAreClaimed is the false positive this check
// produced on a working cluster.
//
// Both load balancers here are created through the Hetzner provider rather
// than by the CCM — the API one in infra/cluster, the ingress one in
// layers/40-ingress — because the CCM refuses to target a control-plane node.
// Neither carries a CCM label, and the absence used to be read as "not this
// cluster's": two findings and exit 1 with nothing wrong. `task destroy` runs
// this check last, so the teardown reported a failure it did not have.
func TestOrphans_PulumiLoadBalancersAreClaimed(t *testing.T) {
	t.Parallel()

	found := Orphans(Inventory{
		LoadBalancers: []LoadBalancer{
			{
				Name: "platform-dev-api",
				Labels: map[string]string{
					clusterspec.LabelCluster:   "platform-dev",
					clusterspec.LabelManagedBy: clusterspec.ManagedBy,
				},
			},
			{
				Name: "platform-dev-ingress",
				Labels: map[string]string{
					clusterspec.LabelCluster:   "platform-dev",
					clusterspec.LabelManagedBy: clusterspec.ManagedBy,
				},
			},
		},
	}, claims())

	assert.Empty(t, found,
		"a load balancer this repository created is claimed by the stack that created it")
}

func TestOrphans_AServerThatIsNotANode(t *testing.T) {
	t.Parallel()

	// The shape a failed image bake leaves: hcloud-upload-image boots a
	// server into rescue mode, and a crash part way through leaves it
	// running and billed with nothing referring to it.
	found := Orphans(Inventory{Servers: []Server{
		{Name: "node-kept", Type: "cx23"},
		{Name: "hcloud-upload-image-leftover", Type: "cx23"},
	}}, claims())

	require.Len(t, found, 1)
	assert.Equal(t, KindServer, found[0].Kind)
	assert.Equal(t, "hcloud-upload-image-leftover", found[0].Name)
	assert.Contains(t, found[0].Why, "cx23")
}

func TestOrphans_AnUnassignedAddress(t *testing.T) {
	t.Parallel()

	// A primary IP is billed while it exists, assigned or not — and a server
	// deleted without its address leaves one behind.
	found := Orphans(Inventory{PrimaryIPs: []PrimaryIP{
		{Name: "assigned", IP: "192.0.2.1", AssigneeID: 42},
		{Name: "loose", IP: "192.0.2.2"},
	}}, claims())

	require.Len(t, found, 1)
	assert.Equal(t, KindPrimaryIP, found[0].Kind)
	assert.Equal(t, "loose", found[0].Name)
	assert.Contains(t, found[0].Why, "192.0.2.2")
}

func TestOrphans_SnapshotsOnlyForAnotherTalosVersion(t *testing.T) {
	t.Parallel()

	// The pinned version is what every server boots from, so only the others
	// are unused. A snapshot with no version label is somebody else's and is
	// left alone — image:bake stamps the label, so its absence means this
	// repository did not create it.
	found := Orphans(Inventory{Snapshots: []Snapshot{
		{Description: "current", SizeGB: 0.2, Labels: map[string]string{TalosVersionLabel: "v1.13.10"}},
		{Description: "previous", SizeGB: 0.2, Labels: map[string]string{TalosVersionLabel: "v1.12.4"}},
		{Description: "somebody else's", SizeGB: 9},
	}}, claims())

	require.Len(t, found, 1)
	assert.Equal(t, KindSnapshot, found[0].Kind)
	assert.Equal(t, "previous", found[0].Name)
	assert.Contains(t, found[0].Why, "v1.12.4")
	assert.Contains(t, found[0].Why, "v1.13.10")
}

func TestOrphans_IsOrderedSoTwoRunsReadTheSame(t *testing.T) {
	t.Parallel()

	// Reported to a person who compares runs. Grouped by kind, then by name.
	found := Orphans(Inventory{
		Volumes: []Volume{{Name: "pvc-b"}, {Name: "pvc-a"}},
		Servers: []Server{{Name: "server-b"}, {Name: "server-a"}},
	}, claims())

	var order []string
	for _, finding := range found {
		order = append(order, finding.Kind+"/"+finding.Name)
	}

	assert.Equal(t, []string{
		"server/server-a", "server/server-b",
		"volume/pvc-a", "volume/pvc-b",
	}, order)
}

func TestReport_SaysSoWhenThereIsNothing(t *testing.T) {
	t.Parallel()

	// "(none)" must not read the same as a failed lookup, which is why the
	// empty case has words rather than an empty table.
	got := Report(nil, Inventory{Volumes: []Volume{{Name: "pvc-kept"}}}.Examined(), "")

	assert.Contains(t, got, "no orphans")
	assert.NotContains(t, got, "KIND")
	// The counts are the point: "clean" and "read nothing" must not print the
	// same line.
	assert.Contains(t, got, "examined 1 volumes")
}

func TestReport_TotalsOnlyProvisionedStorage(t *testing.T) {
	t.Parallel()

	// A snapshot's size is a compressed artefact and a volume's is
	// provisioned block storage. Adding them would print a number that means
	// nothing, so only volumes are totalled.
	inventory := Inventory{
		Volumes:   []Volume{{Name: "pvc-gone", SizeGB: 50}},
		Snapshots: []Snapshot{{Description: "old", SizeGB: 0.2, Labels: map[string]string{TalosVersionLabel: "v1.0.0"}}},
	}

	got := Report(Orphans(inventory, claims()), inventory.Examined(), "")

	assert.Contains(t, got, "50 GiB of provisioned volumes")
	// Both sizes still show per row, in a form each is readable in.
	assert.Contains(t, got, "50 Gi")
	assert.Contains(t, got, "0.2 Gi")
	assert.Equal(t, 2, strings.Count(got, "Gi\n")+strings.Count(got, "Gi "),
		"one size per row, and one in the total")
}

// TestClusterServers_OnlyThisClustersOwn is the decision that lets this check
// run at all without a cluster.
//
// A shared Hetzner project can hold another cluster's servers, and counting
// those would make a destroyed cluster look alive — so the check would refuse
// to run for the same wrong reason, with a different cause.
func TestClusterServers_OnlyThisClustersOwn(t *testing.T) {
	t.Parallel()

	inventory := Inventory{Servers: []Server{
		{Name: "platform-dev-control-plane-0", Labels: map[string]string{clusterspec.LabelCluster: "platform-dev"}},
		{Name: "platform-prod-control-plane-0", Labels: map[string]string{clusterspec.LabelCluster: "platform-prod"}},
		// Somebody else's server, or one from before this repository existed.
		{Name: "unlabelled"},
	}}

	mine := ClusterServers(inventory, "platform-dev")

	require.Len(t, mine, 1)
	assert.Equal(t, "platform-dev-control-plane-0", mine[0].Name)

	assert.Empty(t, ClusterServers(inventory, "platform-staging"),
		"a cluster with no servers must read as gone, not as somebody else's")
	assert.Empty(t, ClusterServers(Inventory{}, "platform-dev"),
		"an empty project holds no cluster")
}

// TestOrphans_WithNoClusterReportsEverythingItLeftBehind is the report an
// operator wants after `task destroy` and could not previously get.
//
// Empty claims are the truth once the cluster is gone, and the judgement
// needed no change for it — what changed is that the check now knows when it
// may trust them.
func TestOrphans_WithNoClusterReportsEverythingItLeftBehind(t *testing.T) {
	t.Parallel()

	// What a teardown can leave: a volume whose PVC outlived its deletion, a
	// load balancer the CCM made for a Service that no longer exists, and an
	// address nothing is assigned to.
	inventory := Inventory{
		Volumes:       []Volume{{Name: "pvc-left-behind", SizeGB: 50}},
		LoadBalancers: []LoadBalancer{{Name: "platform-dev-ingress", Labels: map[string]string{ServiceUIDLabel: "uid-1"}}},
		PrimaryIPs:    []PrimaryIP{{Name: "addr-1", IP: "203.0.113.5"}},
		Snapshots: []Snapshot{{
			Description: "talos", SizeGB: 0.2,
			Labels: map[string]string{TalosVersionLabel: "v1.13.10"},
		}},
	}

	gone := Claims{
		PersistentVolumes: map[string]bool{},
		ServiceUIDs:       map[string]bool{},
		Nodes:             map[string]bool{},
		// The topology still pins a version, so the snapshot every rebuild
		// boots from is NOT an orphan. Deleting it would cost a re-bake and
		// save a quarter of a cent a month.
		TalosVersion: "v1.13.10",
	}

	found := Orphans(inventory, gone)

	kinds := map[string]string{}
	for _, f := range found {
		kinds[f.Kind] = f.Name
	}

	assert.Equal(t, "pvc-left-behind", kinds[KindVolume])
	assert.Equal(t, "platform-dev-ingress", kinds[KindLoadBalancer])
	assert.Equal(t, "addr-1", kinds[KindPrimaryIP])
	assert.NotContains(t, kinds, KindSnapshot,
		"the pinned snapshot is what a rebuild boots from; reporting it invites deleting it")
}

// TestReport_ExplainsAJudgementMadeWithoutACluster keeps the alarming version
// of a correct report from being the one an operator reads.
//
// Every volume and every load balancer listed as claimed by nothing is right
// after a teardown and looks like a catastrophe. The note goes first.
func TestReport_ExplainsAJudgementMadeWithoutACluster(t *testing.T) {
	t.Parallel()

	inventory := Inventory{Volumes: []Volume{{Name: "pvc-left-behind", SizeGB: 50}}}

	got := Report(Orphans(inventory, Claims{}), inventory.Examined(), ClusterGoneNote)

	assert.Contains(t, got, "the cluster is gone")
	assert.Contains(t, got, "left behind")
	// Before the table, not after it.
	assert.Less(t, strings.Index(got, "the cluster is gone"), strings.Index(got, "KIND"))

	// And the ordinary report does not carry it.
	assert.NotContains(t, Report(Orphans(inventory, Claims{}), inventory.Examined(), ""),
		"the cluster is gone")
}
