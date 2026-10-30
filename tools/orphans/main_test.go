package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/report"
)

// args is a full command line.
func args() []string {
	return []string{"dev", "kubeconfig", "topology.yaml", "infra/cluster", "layers/10-node-platform"}
}

// world is what run sees through deps, and what it asked for.
type world struct {
	inventory Inventory
	held      Held
	claims    Claims
	// failing names the dependency that returns errBoom.
	failing string
	asked   []string
	out     bytes.Buffer
}

var errBoom = errors.New("boom")

// fail returns errBoom when the step is the failing one, and records the ask.
func (w *world) fail(step string) error {
	w.asked = append(w.asked, step)
	if w.failing == step {
		return errBoom
	}

	return nil
}

// The dependencies run calls, in the order it calls them.
const (
	stepLogin     = "login"
	stepToken     = "token"
	stepTopology  = "topology"
	stepInventory = "inventory"
	stepHeld      = "held"
	stepClaims    = "claims"
)

func (w *world) deps() deps {
	return deps{
		requireLogin: func([]string) error { return w.fail(stepLogin) },
		token: func(context.Context, string) (string, error) {
			return "token", w.fail(stepToken)
		},
		topology: func(string) (*clusterspec.Topology, error) {
			return &clusterspec.Topology{
				Metadata: clusterspec.MetadataSpec{Name: "platform-dev"},
				Talos:    clusterspec.TalosSpec{Version: "v1.13.10"},
			}, w.fail(stepTopology)
		},
		inventory: func(context.Context, string) (Inventory, error) {
			return w.inventory, w.fail(stepInventory)
		},
		held: func(context.Context, []string) (Held, error) {
			return w.held, w.fail(stepHeld)
		},
		claims: func(context.Context, string) (Claims, error) {
			return w.claims, w.fail(stepClaims)
		},
		out:   &w.out,
		paint: report.Plain,
	}
}

// liveCluster is a project whose cluster is up and claims its one volume.
func liveCluster() *world {
	held := NewHeld()
	held.Stacks = 2

	claims := NewClaims()
	claims.PersistentVolumes["pvc-kept"] = true
	claims.Nodes["platform-dev-control-plane-0"] = true

	return &world{
		inventory: Inventory{
			Volumes: []Volume{{ID: 1, Name: "pvc-kept", SizeGB: 10}},
			Servers: []Server{{
				ID: 2, Name: "platform-dev-control-plane-0",
				Labels: map[string]string{clusterspec.LabelCluster: "platform-dev"},
			}},
		},
		held:   held,
		claims: claims,
	}
}

func TestRun_AClaimedProjectIsClean(t *testing.T) {
	t.Parallel()

	w := liveCluster()

	clean, err := run(args(), w.deps())

	require.NoError(t, err)
	assert.True(t, clean)
	assert.Equal(t, []string{stepLogin, stepToken, stepTopology, stepInventory, stepHeld, stepClaims}, w.asked)
	assert.Equal(t, Report(Header{Stack: "dev", Cluster: "platform-dev", Stacks: 2}, w.inventory, nil, report.Plain),
		w.out.String())
}

func TestRun_AnUnclaimedVolumeIsNotClean(t *testing.T) {
	t.Parallel()

	w := liveCluster()
	w.claims = NewClaims()
	w.claims.Nodes["platform-dev-control-plane-0"] = true

	clean, err := run(args(), w.deps())

	require.NoError(t, err)
	assert.False(t, clean)
	assert.Contains(t, w.out.String(), "pvc-kept")
}

func TestRun_WithNoServersTheClusterIsNotAsked(t *testing.T) {
	t.Parallel()

	w := liveCluster()
	w.inventory.Servers = nil
	w.held = NewHeld()

	clean, err := run(args(), w.deps())

	require.NoError(t, err)
	assert.False(t, clean, "everything left behind is an orphan")
	assert.NotContains(t, w.asked, stepClaims)

	found := Orphans(w.inventory, Claims{
		PersistentVolumes: map[string]bool{}, ReleasedVolumes: map[string]bool{},
		ServiceUIDs: map[string]bool{}, Nodes: map[string]bool{},
		TalosVersion: "v1.13.10", Held: w.held,
	})
	assert.Equal(t,
		Report(Header{Stack: "dev", Cluster: "platform-dev", ClusterGone: true}, w.inventory, found, report.Plain),
		w.out.String())
}

func TestRun_RefusesAnEmptyBackendWhileServersExist(t *testing.T) {
	t.Parallel()

	w := liveCluster()
	w.held = NewHeld()

	_, err := run(args(), w.deps())

	require.ErrorIs(t, err, ErrNoStacks)
	assert.NotContains(t, w.asked, stepClaims)
	assert.Empty(t, w.out.String())
}

func TestRun_AnUnreachableClusterNamesItsServers(t *testing.T) {
	t.Parallel()

	w := liveCluster()
	w.failing = stepClaims

	_, err := run(args(), w.deps())

	require.ErrorIs(t, err, errBoom)
	assert.Equal(t, "boom\n\n1 server(s) still carry cluster=platform-dev, so the cluster should answer. "+
		"If it is gone, its servers are not — and they are billed", err.Error())
	assert.Empty(t, w.out.String())
}

func TestRun_StopsAtTheFirstFailure(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		step, want string
	}{
		{stepLogin, "boom"},
		{stepToken, "boom"},
		{stepTopology, "read the pinned Talos version: boom"},
		{stepInventory, "boom"},
		{stepHeld, "read the stacks' states: boom"},
	} {
		t.Run(tc.step, func(t *testing.T) {
			t.Parallel()

			w := liveCluster()
			w.failing = tc.step

			_, err := run(args(), w.deps())

			require.ErrorIs(t, err, errBoom)
			assert.Equal(t, tc.want, err.Error())
			assert.Equal(t, tc.step, w.asked[len(w.asked)-1], "nothing is asked after a failure")
			assert.Empty(t, w.out.String())
		})
	}
}

func TestRun_RefusesTooFewArguments(t *testing.T) {
	t.Parallel()

	full := args()

	for count := range fixedArgs + 1 {
		w := liveCluster()

		_, err := run(full[:count], w.deps())

		require.ErrorIs(t, err, errUsage, "%d arguments", count)
		assert.Empty(t, w.asked, "nothing is asked before the arguments are")
	}
}

func TestParseArgs_NamesEachArgument(t *testing.T) {
	t.Parallel()

	got, err := parseArgs(args())

	require.NoError(t, err)
	assert.Equal(t, invocation{
		stack: "dev", kubeconfig: "kubeconfig", topology: "topology.yaml",
		dirs: []string{"infra/cluster", "layers/10-node-platform"},
	}, got)
}

func TestExitCode(t *testing.T) {
	t.Parallel()

	assert.Equal(t, exitClean, exitCode(true))
	assert.Equal(t, exitOrphans, exitCode(false))
	assert.NotEqual(t, exitClean, exitOrphans, "a finding has to be told apart from a clean run")
}

// kubectlOutput is a fake commandRunner answering each kubectl listing by
// its kind and template.
type kubectlOutput struct {
	byQuery map[string]string
	failing string
	asked   [][]string
}

func (k *kubectlOutput) run(_ context.Context, name string, args ...string) (stdout, stderr []byte, err error) {
	k.asked = append(k.asked, append([]string{name}, args...))

	// --kubeconfig <path> get <kind> --all-namespaces -o jsonpath=<template>
	kind, output := args[3], args[6]
	if kind == k.failing {
		return nil, []byte("  Unable to connect to the server  \n"), errBoom
	}

	return []byte(k.byQuery[kind+" "+output]), nil, nil
}

func TestReadClaims_FillsEachSetFromItsListing(t *testing.T) {
	t.Parallel()

	fake := &kubectlOutput{byQuery: map[string]string{
		kubePersistentVolumes + " jsonpath=" + namesTemplate:    "pvc-a\npvc-b\n",
		kubePersistentVolumes + " jsonpath=" + releasedTemplate: "pvc-b\n",
		kubeServices + " jsonpath=" + uidsTemplate:              "uid-1\n",
		kubeNodes + " jsonpath=" + namesTemplate:                "node-0\nnode-1\n",
	}}

	got, err := readClaims(t.Context(), "kubeconfig", fake.run)

	require.NoError(t, err)
	assert.Equal(t, Claims{
		PersistentVolumes: map[string]bool{"pvc-a": true, "pvc-b": true},
		ReleasedVolumes:   map[string]bool{"pvc-b": true},
		ServiceUIDs:       map[string]bool{"uid-1": true},
		Nodes:             map[string]bool{"node-0": true, "node-1": true},
	}, got)
	assert.Equal(t, []string{
		"kubectl", "--kubeconfig", "kubeconfig", "get", "persistentvolumes", "--all-namespaces",
		"-o", `jsonpath={range .items[*]}{.metadata.name}{"\n"}{end}`,
	}, fake.asked[0])
	assert.Equal(t, `jsonpath={range .items[?(@.status.phase=="Released")]}{.metadata.name}{"\n"}{end}`,
		fake.asked[1][7])
	assert.Equal(t, `jsonpath={range .items[*]}{.metadata.uid}{"\n"}{end}`, fake.asked[2][7])
}

func TestReadClaims_AnUnreachableClusterIsAnError(t *testing.T) {
	t.Parallel()

	fake := &kubectlOutput{failing: kubeServices}

	_, err := readClaims(t.Context(), "kubeconfig", fake.run)

	require.ErrorIs(t, err, errBoom)
	assert.Equal(t, "kubectl get services: boom: Unable to connect to the server\n"+
		"the cluster has to answer: an empty list would report every resource in the\n"+
		"project as an orphan", err.Error())
	assert.Len(t, fake.asked, 3, "nothing is asked after a failure")
}

func TestNonEmptyLines(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"\n", nil},
		{"a\n", []string{"a"}},
		{"a\n\n b \n", []string{"a", "b"}},
		{"a\nb", []string{"a", "b"}},
	} {
		assert.Equal(t, tc.want, nonEmptyLines(tc.in), "%q", tc.in)
	}
}

func TestExecCommand_ReportsAProgramThatCannotStart(t *testing.T) {
	t.Parallel()

	_, _, err := execCommand(t.Context(), "/nonexistent/kubectl", "version")

	require.Error(t, err)
}
