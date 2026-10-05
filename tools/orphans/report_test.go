package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/report"
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

	got := Report(header(), inventory, nil, report.Plain)

	assert.True(t, strings.HasPrefix(got, "◉ hetzner-iac · cluster:orphans · stack dev\n\n"))
	assert.Contains(t, got, "  cluster  platform-dev\n")
	// The counts are the point: "clean" and "read nothing" must not print the
	// same report.
	assert.Contains(t, got, "  checked  3 resources of 2 kinds · against 7 stack states\n")
	assert.Contains(t, got, "  server   ✔      1\n")
	assert.Contains(t, got, "  network  ✔      2\n")
	assert.True(t, strings.HasSuffix(got, "\n\n  ✔ all 3 resources are claimed\n"))
	assert.NotContains(t, got, report.MarkWarning, "nothing is unclaimed, so nothing warns")
}

func TestReport_NamesTheKindsItFoundNoneOf(t *testing.T) {
	t.Parallel()

	got := Report(header(), Inventory{Servers: []Server{{}}}, nil, report.Plain)

	assert.Contains(t, got, "  absent   volumes, load balancers")
	assert.Contains(t, got, "dns zones\n")
	assert.NotContains(t, got, "  volume ", "an empty kind takes no row")
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
	}, found, report.Plain)

	servers := strings.Index(got, "  ▲ server\n")
	networks := strings.Index(got, "  ▲ network\n")

	require.Positive(t, servers)
	assert.Less(t, servers, networks, "billed kinds first, as the table lists them")
	assert.Contains(t, got, "  server   ▲      2          2\n", "the table counts what is unclaimed per kind")
	// Names align within a section, so the reasons start in one column.
	assert.Contains(t, got, "    upload-leftover                    not a node\n")
	assert.Contains(t, got, "  ▲ 3 of 3 resources are unclaimed\n    Nothing was deleted.")
}

func TestReport_TotalsOnlyProvisionedStorage(t *testing.T) {
	t.Parallel()

	// A snapshot's size is a compressed artefact and a volume's is
	// provisioned block storage. Adding them would print a number that means
	// nothing, so only volumes are totalled.
	inventory := Inventory{
		Volumes:   []Volume{{Name: "pvc-gone", SizeGB: 50}, {Name: "pvc-small", SizeGB: 1}},
		Snapshots: []Snapshot{{Description: "old", SizeGB: 0.2, Labels: map[string]string{TalosVersionLabel: "v1.0.0"}}},
	}

	got := Report(header(), inventory, Orphans(inventory, claims()), report.Plain)

	assert.Contains(t, got, "3 of 3 resources are unclaimed · 51 GiB of provisioned volumes")
	// Sizes are right-aligned within their section.
	assert.Contains(t, got, "    pvc-gone    50 Gi  no PersistentVolume")
	assert.Contains(t, got, "    pvc-small  1.0 Gi  no PersistentVolume")
	assert.Contains(t, got, "    old  0.2 Gi")
}

func TestReport_ExplainsAJudgementMadeWithoutACluster(t *testing.T) {
	t.Parallel()

	gone := header()
	gone.ClusterGone = true

	inventory := Inventory{Volumes: []Volume{{Name: "pvc-left-behind", SizeGB: 50}}}
	found := Orphans(inventory, Claims{})

	got := Report(gone, inventory, found, report.Plain)

	// In the facts, before the table: the list reads as a catastrophe otherwise.
	assert.Contains(t, got, "  cluster  platform-dev · gone: no server carries its label\n")
	assert.Contains(t, got, "The cluster is gone, so this is what the teardown left")

	assert.NotContains(t, Report(header(), inventory, found, report.Plain), "gone: no server")
}

func TestReport_PaintsOnlyThroughThePainter(t *testing.T) {
	t.Parallel()

	found := []Finding{{Kind: KindServer, Name: "leftover", Why: "not a node"}}
	inventory := Inventory{Servers: []Server{{}}}

	assert.NotContains(t, Report(header(), inventory, found, report.Plain), "\x1b[",
		"a pipe or NO_COLOR gets no escape codes")
	assert.Contains(t, Report(header(), inventory, found, report.ANSI), "\x1b["+report.Yellow+"m")
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
