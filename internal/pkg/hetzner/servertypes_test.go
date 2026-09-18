package hetzner_test

import (
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/hetzner"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runWithTypes builds a cluster against a mock that reports `available` as the
// server types the project can create, and returns the error, if any.
func runWithTypes(t *testing.T, topology *clusterspec.Topology, available map[string]string) error {
	t.Helper()

	rec := newRecorder()
	rec.serverTypes = available

	return pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := hetzner.NewCluster(ctx, "test", &hetzner.ClusterArgs{Topology: topology})

		return err
	}, pulumi.WithMocks("hetzner-iac", "test", rec))
}

func TestValidateServerTypes_RejectsATypeTheProjectCannotCreate(t *testing.T) {
	// This is the failure as it actually happened: `pulumi up` created ten
	// resources and then died on the eleventh with "server type cx42 not
	// found", leaving a half-built cluster in state.
	topology := haTopology(t)
	topology.ControlPlane.ServerType = "cx42"

	err := runWithTypes(t, topology, map[string]string{"cx23": "x86", "cx33": "x86", "cpx31": "x86"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cx42")
	// The alternatives, because the next thing anyone needs is which name to use.
	// Sorted, so the message reads the same on every run.
	assert.Contains(t, err.Error(), "cpx31, cx23, cx33")
	// The offered list is scoped to the architecture the image was baked for,
	// so nothing in it is a type that exists and then fails to boot.
	assert.Contains(t, err.Error(), "Available for x86:")
}

func TestValidateServerTypes_AcceptsATypeThatExists(t *testing.T) {
	topology := haTopology(t)
	topology.ControlPlane.ServerType = "cx33"

	require.NoError(t, runWithTypes(t, topology, map[string]string{"cx23": "x86", "cx33": "x86"}))
}

func TestValidateServerTypes_ChecksWorkerPoolsToo(t *testing.T) {
	// A pool's type is as capable of not existing as the control plane's, and
	// it fails later — after the control plane is already up.
	topology := haTopology(t)
	topology.ControlPlane.ServerType = "cx23"
	topology.WorkerPools = []clusterspec.WorkerPoolSpec{
		{Name: "worker", Count: 1, ServerType: "nonsense99"},
	}

	err := runWithTypes(t, topology, map[string]string{"cx23": "x86", "cx33": "x86"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "nonsense99")
}

func TestValidateServerTypes_AnEmptyLookupDoesNotBlockTheDeploy(t *testing.T) {
	// A lookup that returns nothing must not reject every topology. The API is
	// about to be called anyway and will refuse a bad type itself; this check
	// buys an earlier failure, not a new way to fail.
	topology := haTopology(t)
	topology.ControlPlane.ServerType = "cx42"

	require.NoError(t, runWithTypes(t, topology, nil))
}

func TestValidateServerTypes_RejectsATypeFromTheWrongArchitecture(t *testing.T) {
	t.Parallel()

	// The worst case of the three: the type exists, so "not found" would be a
	// lie, and the server would be created and then fail to boot the image —
	// which is a much longer way to learn the same thing.
	topology := haTopology(t)
	topology.Talos.Architecture = "x86"
	topology.ControlPlane.ServerType = "cax31"

	err := runWithTypes(t, topology, map[string]string{
		"cx23":  "x86",
		"cax31": "arm",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cax31 is arm")
	assert.Contains(t, err.Error(), "will not boot")
	// And only the types that can boot are offered.
	assert.Contains(t, err.Error(), "Available for x86: cx23")
	assert.NotContains(t, err.Error(), "Available for x86: cax31")
}
