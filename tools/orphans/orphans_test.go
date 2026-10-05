package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
)

// claims is a cluster that accounts for everything named in it.
func claims() Claims {
	return Claims{
		PersistentVolumes: map[string]bool{"pvc-kept": true},
		ReleasedVolumes:   map[string]bool{},
		ServiceUIDs:       map[string]bool{"uid-kept": true},
		Nodes:             map[string]bool{"node-kept": true},
		TalosVersion:      "v1.13.10",
		Held:              NewHeld(),
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
			why:    "made by neither this repository nor the cluster",
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

// TestOrphans_PulumiLoadBalancersAreClaimedByTheirStack is the false positive
// this check once produced on a working cluster.
//
// Both load balancers here are created through the Hetzner provider rather
// than by the CCM, and carry no CCM label. The stack holding them is what
// claims them.
func TestOrphans_PulumiLoadBalancersAreClaimedByTheirStack(t *testing.T) {
	t.Parallel()

	held := claims()
	held.Held.Add(KindLoadBalancer, 7848680)
	held.Held.Add(KindLoadBalancer, 7848779)

	found := Orphans(Inventory{
		LoadBalancers: []LoadBalancer{
			{ID: 7848680, Name: "platform-dev-api", Labels: ours("platform-dev")},
			{ID: 7848779, Name: "platform-dev-ingress", Labels: ours("platform-dev")},
		},
	}, held)

	assert.Empty(t, found, "a load balancer a stack holds is that stack's to destroy")
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

// TestOrphans_AReleasedVolumeIsReportedThoughItsPVExists is the gap the
// retaining storage class opens.
//
// `reclaimPolicy: Retain` is what a database's volume needs: deleting the claim
// leaves the volume. What it also leaves is a PersistentVolume in phase
// Released that Kubernetes will never bind again by itself — so the object
// exists, the check that asks "does a PersistentVolume of this name exist"
// says the volume is claimed, and the bill runs until somebody looks.
func TestOrphans_AReleasedVolumeIsReportedThoughItsPVExists(t *testing.T) {
	t.Parallel()

	held := claims()
	held.PersistentVolumes["pvc-released"] = true
	held.ReleasedVolumes["pvc-released"] = true

	found := Orphans(Inventory{Volumes: []Volume{
		{Name: "pvc-kept", SizeGB: 50},
		{Name: "pvc-released", SizeGB: 100},
	}}, held)

	require.Len(t, found, 1, "the bound volume must not be reported beside the released one")
	assert.Equal(t, KindVolume, found[0].Kind)
	assert.Equal(t, "pvc-released", found[0].Name)
	assert.InDelta(t, 100, found[0].Size, 0, "the size is what makes the bill legible")
	assert.Contains(t, found[0].Why, PhaseReleased)
	assert.NotContains(t, found[0].Why, "retained",
		"the message must fit both ways a volume reaches this state: retained on purpose by "+
			"the database class, or not yet deleted by the driver on the default one — both "+
			"appeared in one live run")
}

// TestOrphans_AReleasedVolumeIsNotReportedTwice: the two volume rules are
// exclusive, and a volume in both lists would otherwise appear as an orphan
// AND as released, which reads as two problems.
func TestOrphans_AReleasedVolumeIsNotReportedTwice(t *testing.T) {
	t.Parallel()

	held := claims()
	held.ReleasedVolumes["pvc-released"] = true

	found := Orphans(Inventory{Volumes: []Volume{{Name: "pvc-released", SizeGB: 10}}}, held)

	require.Len(t, found, 1)
	assert.Contains(t, found[0].Why, PhaseReleased)
}

// TestOrphans_ALabelledResourceNoStackHoldsIsReported is the bill the label
// alone used to hide.
//
// The label says this repository made it; only a state says a stack will
// destroy it. A destroy that stopped part-way, a stack removed with --force,
// or a layer destroyed while the cluster lives leaves exactly this.
func TestOrphans_ALabelledResourceNoStackHoldsIsReported(t *testing.T) {
	t.Parallel()

	found := Orphans(Inventory{
		LoadBalancers: []LoadBalancer{{ID: 1, Name: "platform-dev-ingress", Labels: ours("platform-dev")}},
	}, claims())

	require.Len(t, found, 1, "the cluster being alive does not make a stack hold it")
	assert.Equal(t, "platform-dev-ingress", found[0].Name)
	assert.Contains(t, found[0].Why, "made by this repository for platform-dev")
	assert.Contains(t, found[0].Why, "no stack holds it")
}

// TestOrphans_StackOnlyKinds covers every kind nothing in the cluster can
// claim: held by a stack, or reported with who made it.
func TestOrphans_StackOnlyKinds(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{
		KindFloatingIP, KindNetwork, KindFirewall, KindPlacementGroup, KindSSHKey,
		KindCertificate, KindStorageBox, KindSubaccount, KindZone,
	} {
		held := claims()
		held.Held.Add(kind, 1)

		found := Orphans(Inventory{Resources: []Resource{
			{Kind: kind, ID: 1, Name: "held"},
			{Kind: kind, ID: 2, Name: "left-behind", Labels: ours("platform-dev")},
			{Kind: kind, ID: 3, Name: "by-hand"},
		}}, held)

		require.Len(t, found, 2, kind)
		assert.Equal(t, kind, found[0].Kind, kind)
		assert.Equal(t, "by-hand", found[0].Name, kind)
		assert.Contains(t, found[0].Why, "made by neither this repository nor the cluster", kind)
		assert.Equal(t, "left-behind", found[1].Name, kind)
		assert.Contains(t, found[1].Why, "made by this repository for platform-dev", kind)
	}
}

// TestOrphans_HeldIsPerKind: the API numbers each kind on its own, so a
// server's ID can be a network's too.
func TestOrphans_HeldIsPerKind(t *testing.T) {
	t.Parallel()

	held := claims()
	held.Held.Add(KindServer, 42)

	found := Orphans(Inventory{Resources: []Resource{{Kind: KindNetwork, ID: 42, Name: "net"}}}, held)

	require.Len(t, found, 1)
	assert.Equal(t, KindNetwork, found[0].Kind)
}

// TestOrphans_AZoneIsClaimedByTheRecordsWrittenToIt: the zone is made by
// hand and delegated, and a stack writes record sets into it.
func TestOrphans_AZoneIsClaimedByTheRecordsWrittenToIt(t *testing.T) {
	t.Parallel()

	held := claims()
	held.Held.Zones["example.com"] = true
	// The provider takes the zone's ID as well as its name.
	held.Held.Zones["3"] = true

	found := Orphans(Inventory{Resources: []Resource{
		{Kind: KindZone, ID: 1, Name: "example.com"},
		{Kind: KindZone, ID: 2, Name: "example.org"},
		{Kind: KindZone, ID: 3, Name: "example.net"},
	}}, held)

	require.Len(t, found, 1)
	assert.Equal(t, "example.org", found[0].Name)
}

func TestRefuseEmptyBackend(t *testing.T) {
	t.Parallel()

	require.ErrorIs(t, RefuseEmptyBackend(3, 0), ErrNoStacks,
		"servers and no stack anywhere: the wrong backend would call everything unheld")
	require.NoError(t, RefuseEmptyBackend(0, 0), "after a teardown, no stacks is the truth")
	require.NoError(t, RefuseEmptyBackend(3, 7))
	require.NoError(t, RefuseEmptyBackend(0, 7))
}

// TestOrphans_AStackClaimsWhatTheClusterDoesNot: a held resource is the
// stack's to destroy, whatever the cluster's own claims say.
func TestOrphans_AStackClaimsWhatTheClusterDoesNot(t *testing.T) {
	t.Parallel()

	held := claims()
	held.Held.Add(KindServer, 1)
	held.Held.Add(KindPrimaryIP, 2)
	held.Held.Add(KindVolume, 3)
	held.Held.Add(KindSnapshot, 4)

	found := Orphans(Inventory{
		// A server that has not joined yet.
		Servers: []Server{{ID: 1, Name: "platform-dev-control-plane-0"}},
		// An address held across a server's replacement.
		PrimaryIPs: []PrimaryIP{{ID: 2, Name: "platform-dev-control-plane-0", IP: "192.0.2.1"}},
		Volumes:    []Volume{{ID: 3, Name: "made-by-a-stack", SizeGB: 10}},
		Snapshots: []Snapshot{{
			ID: 4, Description: "made-by-a-stack", Labels: map[string]string{TalosVersionLabel: "v1.0.0"},
		}},
	}, held)

	assert.Empty(t, found)
}

// ours is the label set this repository stamps on what it creates.
func ours(cluster string) map[string]string {
	return map[string]string{
		clusterspec.LabelCluster:   cluster,
		clusterspec.LabelManagedBy: clusterspec.ManagedBy,
	}
}
