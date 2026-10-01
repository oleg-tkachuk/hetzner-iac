package stackdrift_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/stackdrift"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/stackstatus"
)

const stack = "dev"

var (
	policy = stackdrift.Change{
		Type: "kubernetes:cilium.io/v2:CiliumClusterwideNetworkPolicy", Name: "allow-hcloud-metadata",
		Op: stackdrift.OpUpdate, Fields: []string{"spec.description"},
	}
	firewall = stackdrift.Change{Type: "hcloud:index/firewall:Firewall", Name: "platform-dev-firewall", Op: stackdrift.OpDelete}
)

func render(t *testing.T, projects []stackdrift.Project) string {
	t.Helper()

	var out strings.Builder
	require.NoError(t, stackdrift.Render(&out, stack, projects, stackstatus.Plain))

	return out.String()
}

func TestDrifted(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		projects []stackdrift.Project
		want     bool
	}{
		{"every stack matches", []stackdrift.Project{{Name: "cluster", HasStack: true}, {Name: "backup"}}, false},
		{"a changed resource", []stackdrift.Project{{Name: "20-network-policy", HasStack: true, Changes: []stackdrift.Change{policy}}}, true},
		{"an unread stack is not a clean one", []stackdrift.Project{{Name: "cluster", Err: errors.New("timeout")}}, true},
	} {
		assert.Equal(t, tc.want, stackdrift.Drifted(tc.projects), tc.name)
	}
}

func TestRender_NamesWhatChangedAndHow(t *testing.T) {
	t.Parallel()

	report := render(t, []stackdrift.Project{
		{Name: "cluster", HasStack: true, Changes: []stackdrift.Change{firewall}},
		{Name: "backup"},
		{Name: "20-network-policy", HasStack: true, Changes: []stackdrift.Change{policy}},
		{Name: "50-gitops", HasStack: true},
	})

	assert.Contains(t, report, "- hcloud:index/firewall:Firewall platform-dev-firewall — gone from the cloud")
	assert.Contains(t, report, "~ kubernetes:cilium.io/v2:CiliumClusterwideNetworkPolicy allow-hcloud-metadata — spec.description")
	assert.Contains(t, report, "no dev stack")
	assert.Contains(t, report, "50-gitops          matches the cloud", "names are padded to one column")
	assert.Contains(t, report, "2 resource(s) changed outside Pulumi")
}

func TestRender_SaysWhenDriftIsUnknown(t *testing.T) {
	t.Parallel()

	report := render(t, []stackdrift.Project{
		{Name: "cluster", HasStack: true},
		{Name: "40-ingress", Err: errors.New("dial tcp: i/o timeout")},
	})

	assert.Contains(t, report, "40-ingress  dial tcp: i/o timeout")
	assert.Contains(t, report, "1 stack(s) could not be read")
	assert.NotContains(t, report, "every stack matches the cloud")
}

func TestRender_Clean(t *testing.T) {
	t.Parallel()

	assert.Contains(t, render(t, []stackdrift.Project{{Name: "cluster", HasStack: true}}), "every stack matches the cloud")
}
