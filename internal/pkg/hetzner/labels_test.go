package hetzner_test

import (
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/hetzner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResourceLabels(t *testing.T) {
	t.Parallel()

	labels := hetzner.ResourceLabels("platform-hel", map[string]string{
		hetzner.LabelRole: hetzner.RoleWorker,
		hetzner.LabelPool: "gpu",
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
	labels := hetzner.ResourceLabels("platform-hel", map[string]string{
		hetzner.LabelCluster: "somewhere-else",
	})

	assert.Equal(t, "platform-hel", labels[hetzner.LabelCluster])
}

func TestClusterSelector(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "cluster=platform-hel", hetzner.ClusterSelector("platform-hel"))
}

func TestTalosImageSelector(t *testing.T) {
	t.Parallel()

	// Pinned to the literal, because this is what `cluster:image:bake` stamped
	// on the snapshot already sitting in the project. Deriving it differently
	// here would not fail here — it would find no image at plan time, with the
	// remedy being to re-bake a snapshot that already exists.
	assert.Equal(t, "os=talos,talos-version=v1.13.10", hetzner.TalosImageSelector("v1.13.10"))
}

func TestTalosImageSelector_CarriesNoArchitectureTerm(t *testing.T) {
	t.Parallel()

	// The architecture is a first-class Hetzner field, filtered by both sides
	// separately. Were it a label term here too, the snapshot would hold the
	// fact twice and the two copies would be free to disagree.
	for _, arch := range hetzner.Architectures {
		assert.NotContains(t, hetzner.TalosImageSelector("v1.13.10"), arch)
	}
}

func TestBuildFirewallRules_Baseline(t *testing.T) {
	t.Parallel()

	admin := []string{"203.0.113.4/32"}

	rules, err := hetzner.BuildFirewallRules(admin, hetzner.FirewallRuleOptions{})
	require.NoError(t, err)
	require.Len(t, rules, 2)

	assert.Equal(t, "kube-apiserver", rules[0].Description)
	assert.Equal(t, "6443", rules[0].Port)
	assert.Equal(t, admin, rules[0].SourceIPs)

	assert.Equal(t, "talos apid", rules[1].Description)
	assert.Equal(t, "50000", rules[1].Port)
}

func TestBuildFirewallRules_ICMPOptional(t *testing.T) {
	t.Parallel()

	admin := []string{"203.0.113.4/32"}

	without, err := hetzner.BuildFirewallRules(admin, hetzner.FirewallRuleOptions{})
	require.NoError(t, err)

	with, err := hetzner.BuildFirewallRules(admin, hetzner.FirewallRuleOptions{AllowICMP: true})
	require.NoError(t, err)

	assert.Len(t, with, len(without)+1)
	assert.Equal(t, "icmp", with[len(with)-1].Protocol)
	assert.Empty(t, with[len(with)-1].Port, "icmp rules must not carry a port")
}

func TestBuildFirewallRules_RefusesEmptyAdminCIDRs(t *testing.T) {
	t.Parallel()

	_, err := hetzner.BuildFirewallRules(nil, hetzner.FirewallRuleOptions{})
	require.ErrorIs(t, err, hetzner.ErrEmptyAdminCIDRs)
}

func TestBuildFirewallRules_RejectsMalformedExtraRules(t *testing.T) {
	t.Parallel()

	admin := []string{"203.0.113.4/32"}

	tests := []struct {
		name    string
		extra   hetzner.FirewallRule
		wantMsg string
	}{
		{
			name:    "tcp without a port",
			extra:   hetzner.FirewallRule{Description: "nodeport", Protocol: "tcp", SourceIPs: admin},
			wantMsg: "needs a port",
		},
		{
			name:    "icmp with a port",
			extra:   hetzner.FirewallRule{Description: "ping", Protocol: "icmp", Port: "0", SourceIPs: admin},
			wantMsg: "must not carry port",
		},
		{
			name:    "no sources",
			extra:   hetzner.FirewallRule{Description: "orphan", Protocol: "tcp", Port: "80"},
			wantMsg: "has no source CIDRs",
		},
		{
			name:    "unknown protocol",
			extra:   hetzner.FirewallRule{Description: "mystery", Protocol: "sctp", Port: "80", SourceIPs: admin},
			wantMsg: "unsupported protocol",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := hetzner.BuildFirewallRules(admin, hetzner.FirewallRuleOptions{
				Extra: []hetzner.FirewallRule{tc.extra},
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantMsg)
		})
	}
}

func TestNodeName(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "platform-hel-control-plane-0", hetzner.NodeName("platform-hel", "control-plane", 0))
	assert.Equal(t, "platform-hel-worker-11", hetzner.NodeName("platform-hel", "worker", 11))
}

func TestSortedLabelPairs_IsDeterministic(t *testing.T) {
	t.Parallel()

	labels := map[string]string{"z": "1", "a": "2", "m": "3"}

	// Map iteration order is randomised per run; unsorted output would show
	// up as a resource diff on runs where nothing changed.
	for range 20 {
		assert.Equal(t, []string{"a=2", "m=3", "z=1"}, hetzner.SortedLabelPairs(labels))
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

			key, value, effect, err := hetzner.ParseTaint(tc.in)
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
