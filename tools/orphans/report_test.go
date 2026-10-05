package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/stackstatus"
)

func header() Header {
	return Header{Stack: "dev", Cluster: "platform-dev", Stacks: 7}
}

func TestReport_ACleanProjectShowsWhatWasRead(t *testing.T) {
	t.Parallel()

	inventory := Inventory{
		Servers:   []Server{{Name: "node-kept"}},
		Resources: []Resource{{Kind: KindNetwork}, {Kind: KindNetwork}},
	}

	got := Report(header(), inventory, nil, stackstatus.Plain)

	assert.Contains(t, got, "orphans · stack dev · cluster platform-dev · 7 stack state(s) read")
	// The counts are the point: "clean" and "read nothing" must not print the
	// same report.
	assert.Regexp(t, `server\s+1\n`, got)
	assert.Regexp(t, `network\s+2\n`, got)
	assert.Contains(t, got, "all 3 resources are claimed")
	assert.NotContains(t, got, stackstatus.MarkWarning, "nothing is unclaimed, so nothing warns")
}

func TestReport_NamesTheKindsItFoundNoneOf(t *testing.T) {
	t.Parallel()

	got := Report(header(), Inventory{Servers: []Server{{}}}, nil, stackstatus.Plain)

	assert.Contains(t, got, "none of: volumes, load balancers")
	assert.Contains(t, got, "dns zones")
	assert.NotRegexp(t, `\n  volume\s`, got, "an empty kind takes no row")
}

func TestReport_GroupsFindingsByKindInTheTablesOrder(t *testing.T) {
	t.Parallel()

	found := []Finding{
		{Kind: KindNetwork, Name: "platform-old", Why: "no stack holds it"},
		{Kind: KindServer, Name: "upload-leftover", Why: "not a node"},
		{Kind: KindServer, Name: "another-leftover-with-a-long-name", Why: "not a node"},
	}

	got := Report(header(), Inventory{
		Servers:   []Server{{}, {}},
		Resources: []Resource{{Kind: KindNetwork}},
	}, found, stackstatus.Plain)

	servers := strings.Index(got, "▲ server\n")
	networks := strings.Index(got, "▲ network\n")

	require.Positive(t, servers)
	assert.Less(t, servers, networks, "billed kinds first, as the table lists them")
	assert.Regexp(t, `server\s+2\s+2\n`, got, "the table counts what is unclaimed per kind")
	// Names align within a group, so the reasons start in one column.
	assert.Contains(t, got, "    upload-leftover                    not a node\n")
	assert.Contains(t, got, "3 of 3 resources are unclaimed")
	assert.Contains(t, got, "Nothing was deleted")
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

	got := Report(header(), inventory, Orphans(inventory, claims()), stackstatus.Plain)

	assert.Contains(t, got, "2 of 2 resources are unclaimed · 50 GiB of provisioned volumes")
	// Each size still shows, in the form it is readable in.
	assert.Contains(t, got, "50 Gi")
	assert.Contains(t, got, "0.2 Gi")
}

func TestReport_ExplainsAJudgementMadeWithoutACluster(t *testing.T) {
	t.Parallel()

	gone := header()
	gone.ClusterGone = true

	inventory := Inventory{Volumes: []Volume{{Name: "pvc-left-behind", SizeGB: 50}}}
	found := Orphans(inventory, Claims{})

	got := Report(gone, inventory, found, stackstatus.Plain)

	assert.Contains(t, got, "the cluster is gone")
	// Before the table, not after it: the list reads as a catastrophe otherwise.
	assert.Less(t, strings.Index(got, "the cluster is gone"), strings.Index(got, headKind))

	assert.NotContains(t, Report(header(), inventory, found, stackstatus.Plain), "the cluster is gone")
}

func TestReport_PaintsOnlyThroughThePainter(t *testing.T) {
	t.Parallel()

	found := []Finding{{Kind: KindServer, Name: "leftover", Why: "not a node"}}
	inventory := Inventory{Servers: []Server{{}}}

	assert.NotContains(t, Report(header(), inventory, found, stackstatus.Plain), "\x1b[",
		"a pipe or NO_COLOR gets no escape codes")
	assert.Contains(t, Report(header(), inventory, found, stackstatus.ANSI), "\x1b["+stackstatus.Yellow+"m")
}

func TestFormatSize(t *testing.T) {
	t.Parallel()

	for size, want := range map[float64]string{0: "", 0.2: "0.2 Gi", 9.95: "9.9 Gi", 10: "10 Gi", 50: "50 Gi"} {
		assert.Equal(t, want, formatSize(size), "%v", size)
	}
}

func TestKindOrder_ListsEveryKindOnce(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for _, kind := range kindOrder() {
		assert.False(t, seen[kind.kind], "%s listed twice", kind.kind)
		seen[kind.kind] = true
	}

	for _, kind := range heldKinds() {
		assert.True(t, seen[kind], "%s is held by stacks but the report never lists it", kind)
	}
}
