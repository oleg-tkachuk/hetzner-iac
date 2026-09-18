package clusterspec_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
)

func TestResourceLabels(t *testing.T) {
	t.Parallel()

	labels := clusterspec.ResourceLabels("platform-hel", map[string]string{
		clusterspec.LabelRole: clusterspec.RoleWorker,
		clusterspec.LabelPool: "gpu",
	})

	assert.Equal(t, map[string]string{
		"cluster":    "platform-hel",
		"managed-by": "hetzner-iac",
		"role":       "worker",
		"pool":       "gpu",
	}, labels)
}

func TestResourceLabels_ClusterCannotBeOverridden(t *testing.T) {
	t.Parallel()

	// The cluster label is what the firewall selector matches. Letting a
	// caller overwrite it would detach the server from the perimeter while
	// still looking like a normal label override.
	labels := clusterspec.ResourceLabels("platform-hel", map[string]string{
		clusterspec.LabelCluster: "somewhere-else",
	})

	assert.Equal(t, "platform-hel", labels[clusterspec.LabelCluster])
}

func TestClusterSelector(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "cluster=platform-hel", clusterspec.ClusterSelector("platform-hel"))
}

func TestTalosImageSelector(t *testing.T) {
	t.Parallel()

	// Pinned to the literal, because this is what `cluster:image:bake` stamped
	// on the snapshot already sitting in the project. Deriving it differently
	// here would not fail here — it would find no image at plan time, with the
	// remedy being to re-bake a snapshot that already exists.
	assert.Equal(t, "os=talos,talos-version=v1.13.10", clusterspec.TalosImageSelector("v1.13.10"))
}

func TestTalosImageSelector_CarriesNoArchitectureTerm(t *testing.T) {
	t.Parallel()

	// The architecture is a first-class Hetzner field, filtered by both sides
	// separately. Were it a label term here too, the snapshot would hold the
	// fact twice and the two copies would be free to disagree.
	for _, arch := range clusterspec.Architectures {
		assert.NotContains(t, clusterspec.TalosImageSelector("v1.13.10"), arch)
	}
}

func TestNodeName(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "platform-hel-control-plane-0", clusterspec.NodeName("platform-hel", "control-plane", 0))
	assert.Equal(t, "platform-hel-worker-11", clusterspec.NodeName("platform-hel", "worker", 11))
}

func TestSortedLabelPairs_IsDeterministic(t *testing.T) {
	t.Parallel()

	labels := map[string]string{"z": "1", "a": "2", "m": "3"}

	// Map iteration order is randomised per run; unsorted output would show
	// up as a resource diff on runs where nothing changed.
	for range 20 {
		assert.Equal(t, []string{"a=2", "m=3", "z=1"}, clusterspec.SortedLabelPairs(labels))
	}
}

func TestParseTaint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in                 string
		key, value, effect string
		wantErr            bool
	}{
		{in: "gpu=true:NoSchedule", key: "gpu", value: "true", effect: "NoSchedule"},
		{in: "dedicated=:NoExecute", key: "dedicated", value: "", effect: "NoExecute"},
		{in: "node.kubernetes.io/role=edge:PreferNoSchedule", key: "node.kubernetes.io/role", value: "edge", effect: "PreferNoSchedule"},
		{in: "malformed", wantErr: true},
		{in: "no-equals:NoSchedule", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()

			key, value, effect, err := clusterspec.ParseTaint(tc.in)
			if tc.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.key, key)
			assert.Equal(t, tc.value, value)
			assert.Equal(t, tc.effect, effect)
		})
	}
}
