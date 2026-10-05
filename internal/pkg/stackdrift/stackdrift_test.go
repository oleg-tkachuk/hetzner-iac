package stackdrift_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/report"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/stackdrift"
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
	require.NoError(t, stackdrift.Render(&out, stack, projects, report.Plain))

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

	assert.Contains(t, report, "◉ hetzner-iac · platform:drift · stack dev\n")
	assert.Contains(t, report, "  cluster            ▲  1 resource differs\n")
	assert.Contains(t, report, "  backup             ○  no dev stack\n")
	assert.Contains(t, report, "  50-gitops          ✔  matches\n", "names are padded to one column")

	// A section per drifted stack, its lines aligned in columns.
	assert.Contains(t, report, "  ▲ cluster\n    - hcloud:index/firewall:Firewall  platform-dev-firewall  gone from the cloud\n")
	assert.Contains(t, report,
		"  ▲ 20-network-policy\n    ~ kubernetes:cilium.io/v2:CiliumClusterwideNetworkPolicy  allow-hcloud-metadata  spec.description\n")
	assert.NotContains(t, report, "▲ 50-gitops", "a stack that matches gets no section")

	assert.True(t, strings.HasSuffix(report, "  ▲ 2 resources changed outside Pulumi\n"+
		"    A refresh adopts the cloud's version into the state; an apply puts the code's back.\n"),
		"the verdict closes the report")
}

func TestRender_SaysWhenDriftIsUnknown(t *testing.T) {
	t.Parallel()

	report := render(t, []stackdrift.Project{
		{Name: "cluster", HasStack: true},
		{Name: "40-ingress", Err: errors.New("dial tcp: i/o timeout")},
	})

	assert.Contains(t, report, "  40-ingress  ✖  dial tcp: i/o timeout\n")
	assert.Contains(t, report, "  ✖ 1 stack could not be read, so drift is unknown there\n")
	assert.NotContains(t, report, "every stack matches the cloud")
}

func TestRender_Clean(t *testing.T) {
	t.Parallel()

	report := render(t, []stackdrift.Project{{Name: "cluster", HasStack: true}})

	assert.Contains(t, report, "  checked  1 stack · each state against the cloud, by refresh preview · nothing written\n")
	assert.True(t, strings.HasSuffix(report, "\n\n  ✔ every stack matches the cloud\n"))
}
