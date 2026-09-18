package clusterspec_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
)

func TestBuildFirewallRules_Baseline(t *testing.T) {
	t.Parallel()

	admin := []string{"203.0.113.4/32"}

	rules, err := clusterspec.BuildFirewallRules(admin, clusterspec.FirewallRuleOptions{})
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

	without, err := clusterspec.BuildFirewallRules(admin, clusterspec.FirewallRuleOptions{})
	require.NoError(t, err)

	with, err := clusterspec.BuildFirewallRules(admin, clusterspec.FirewallRuleOptions{AllowICMP: true})
	require.NoError(t, err)

	assert.Len(t, with, len(without)+1)
	assert.Equal(t, "icmp", with[len(with)-1].Protocol)
	assert.Empty(t, with[len(with)-1].Port, "icmp rules must not carry a port")
}

func TestBuildFirewallRules_RefusesEmptyAdminCIDRs(t *testing.T) {
	t.Parallel()

	_, err := clusterspec.BuildFirewallRules(nil, clusterspec.FirewallRuleOptions{})
	require.ErrorIs(t, err, clusterspec.ErrEmptyAdminCIDRs)
}

func TestBuildFirewallRules_RejectsMalformedExtraRules(t *testing.T) {
	t.Parallel()

	admin := []string{"203.0.113.4/32"}

	tests := []struct {
		name    string
		extra   clusterspec.FirewallRule
		wantMsg string
	}{
		{
			name:    "tcp without a port",
			extra:   clusterspec.FirewallRule{Description: "nodeport", Protocol: "tcp", SourceIPs: admin},
			wantMsg: "needs a port",
		},
		{
			name:    "icmp with a port",
			extra:   clusterspec.FirewallRule{Description: "ping", Protocol: "icmp", Port: "0", SourceIPs: admin},
			wantMsg: "must not carry port",
		},
		{
			name:    "no sources",
			extra:   clusterspec.FirewallRule{Description: "orphan", Protocol: "tcp", Port: "80"},
			wantMsg: "has no source CIDRs",
		},
		{
			name:    "unknown protocol",
			extra:   clusterspec.FirewallRule{Description: "mystery", Protocol: "sctp", Port: "80", SourceIPs: admin},
			wantMsg: "unsupported protocol",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := clusterspec.BuildFirewallRules(admin, clusterspec.FirewallRuleOptions{
				Extra: []clusterspec.FirewallRule{tc.extra},
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantMsg)
		})
	}
}
