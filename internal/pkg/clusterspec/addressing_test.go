package clusterspec_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
)

func TestNewAddressing_Rejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		subnet  string
		stride  int
		wantMsg string
	}{
		{"not a CIDR", "10.0.1.0", 40, "node subnet"},
		{"IPv6", "fd00::/64", 40, "must be IPv4"},
		{"stride too small", "10.0.1.0/24", 1, "too small"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := clusterspec.NewAddressing(tc.subnet, tc.stride)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantMsg)
		})
	}
}

func TestAddressing_ControlPlane(t *testing.T) {
	t.Parallel()

	addressing, err := clusterspec.NewAddressing("10.0.1.0/24", 40)
	require.NoError(t, err)

	// Offsets 0 and 1 are the network address and the Hetzner gateway, so the
	// first control-plane node lands on .2.
	for ordinal, want := range map[int]string{0: "10.0.1.2", 1: "10.0.1.3", 2: "10.0.1.4"} {
		got, ipErr := addressing.ControlPlaneIP(ordinal)
		require.NoError(t, ipErr)
		assert.Equal(t, want, got)
	}

	gateway, err := addressing.Gateway()
	require.NoError(t, err)
	assert.Equal(t, "10.0.1.1", gateway)
}

func TestAddressing_WorkerPoolsDoNotOverlapControlPlane(t *testing.T) {
	t.Parallel()

	addressing, err := clusterspec.NewAddressing("10.0.1.0/24", 40)
	require.NoError(t, err)

	first, err := addressing.WorkerIP(0, 0)
	require.NoError(t, err)
	assert.Equal(t, "10.0.1.40", first)

	second, err := addressing.WorkerIP(1, 0)
	require.NoError(t, err)
	assert.Equal(t, "10.0.1.80", second)

	// The property that matters: every address is unique across the control
	// plane and every pool. A collision would mean two Talos nodes configured
	// with the same address — which does not fail at apply, it fails as a
	// cluster that half-works.
	seen := map[string]string{}

	record := func(who, addr string) {
		if prev, clash := seen[addr]; clash {
			t.Fatalf("address %s assigned to both %s and %s", addr, prev, who)
		}

		seen[addr] = who
	}

	gateway, err := addressing.Gateway()
	require.NoError(t, err)
	record("gateway", gateway)

	for ordinal := range 3 {
		addr, err := addressing.ControlPlaneIP(ordinal)
		require.NoError(t, err)
		record("control-plane", addr)
	}

	for pool := range 5 {
		for ordinal := range 40 {
			addr, err := addressing.WorkerIP(pool, ordinal)
			require.NoError(t, err)
			record("worker", addr)
		}
	}

	assert.Len(t, seen, 1+3+5*40)
}

func TestAddressing_StableUnderGrowth(t *testing.T) {
	t.Parallel()

	// The reason slices are fixed rather than packed: growing pool 0 must not
	// move any node in pool 1. A moved address replaces the node.
	addressing, err := clusterspec.NewAddressing("10.0.1.0/24", 40)
	require.NoError(t, err)

	before, err := addressing.WorkerIP(1, 0)
	require.NoError(t, err)

	// Nothing about allocation depends on how many nodes a pool currently
	// has, so pool 1 is unaffected by pool 0 growing to its full slice.
	last, err := addressing.WorkerIP(0, 39)
	require.NoError(t, err)
	assert.Equal(t, "10.0.1.79", last)

	after, err := addressing.WorkerIP(1, 0)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestAddressing_RefusesOutOfRange(t *testing.T) {
	t.Parallel()

	addressing, err := clusterspec.NewAddressing("10.0.1.0/24", 40)
	require.NoError(t, err)

	tests := []struct {
		name    string
		call    func() (string, error)
		wantMsg string
	}{
		{
			name:    "control-plane ordinal past its slice",
			call:    func() (string, error) { return addressing.ControlPlaneIP(38) },
			wantMsg: "does not fit in a 40-address slice",
		},
		{
			name:    "worker ordinal past its slice",
			call:    func() (string, error) { return addressing.WorkerIP(0, 40) },
			wantMsg: "does not fit in a 40-address slice",
		},
		{
			name:    "pool index past the subnet",
			call:    func() (string, error) { return addressing.WorkerIP(20, 0) },
			wantMsg: "outside node subnet",
		},
		{
			name:    "negative control-plane ordinal",
			call:    func() (string, error) { return addressing.ControlPlaneIP(-1) },
			wantMsg: "negative",
		},
		{
			name:    "negative pool index",
			call:    func() (string, error) { return addressing.WorkerIP(-1, 0) },
			wantMsg: "negative",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := tc.call()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantMsg)
		})
	}
}

func TestAddressing_DoesNotWrapAroundIntoTheSubnet(t *testing.T) {
	t.Parallel()

	// A huge pool index must be refused, not silently wrapped by uint32
	// arithmetic into an address that collides with a real node.
	addressing, err := clusterspec.NewAddressing("10.0.1.0/24", 40)
	require.NoError(t, err)

	_, err = addressing.WorkerIP(1<<26, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside node subnet")
}

func TestAddressing_NormalisesHostBits(t *testing.T) {
	t.Parallel()

	// A subnet written with host bits set still allocates from the network
	// address, so .5 does not shift every node by five.
	addressing, err := clusterspec.NewAddressing("10.0.1.5/24", 40)
	require.NoError(t, err)

	addr, err := addressing.ControlPlaneIP(0)
	require.NoError(t, err)
	assert.Equal(t, "10.0.1.2", addr)
}
