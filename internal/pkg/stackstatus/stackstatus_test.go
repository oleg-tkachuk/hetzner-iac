package stackstatus_test

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/report"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/stackstatus"
)

const (
	env       = "dev"
	headSHA   = "1495bea4c6a1c0de5a1b6b1f0d3c0de5a1b6b1f0"
	otherSHA  = "6963f05f00000000000000000000000000000000"
	clusterAt = "hetzner-cluster/" + env
	goodRef   = "acme/" + clusterAt
	contract  = 3
	cluster   = "cluster"
	ingress   = "40-ingress"
)

var (
	now           = time.Date(2026, time.September, 29, 12, 0, 0, 0, time.UTC)
	errUnreadable = errors.New("could not export: 403 forbidden\nsecond line")
)

func intPtr(n int) *int { return &n }

func header() stackstatus.Header {
	return stackstatus.Header{
		Stack: env, Backend: "https://api.pulumi.com", User: "oleg", PulumiVersion: "3.265.0",
		Head:         stackstatus.Commit{SHA: headSHA, Branch: "main"},
		WantContract: contract,
		Cluster: stackstatus.Cluster{
			Name: "platform-dev", Location: "hel1", Endpoint: "https://203.0.113.4:6443",
			Contract: intPtr(contract), Stack: clusterAt,
		},
	}
}

func succeeded(sha string, changes map[string]int) *stackstatus.Update {
	start := now.Add(-6 * time.Minute)

	return &stackstatus.Update{
		Number: 42, Kind: "update", Result: stackstatus.ResultSucceeded,
		Start: start, End: start.Add(2 * time.Second),
		Changes: changes, Commit: stackstatus.Commit{SHA: sha},
	}
}

// healthy is a consumer whose stack agrees with the cluster in every way.
func healthy(name string) stackstatus.Project {
	return stackstatus.Project{
		Name: name, HasStack: true, Resources: intPtr(4), Last: succeeded(headSHA, nil),
		ClusterRef: goodRef, Contract: intPtr(contract),
	}
}

func render(t *testing.T, h stackstatus.Header, projects []stackstatus.Project) string {
	t.Helper()

	var out strings.Builder

	require.NoError(t, stackstatus.Render(&out, h, projects, now, report.Plain))

	return out.String()
}

func TestCommitShort(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		commit stackstatus.Commit
		want   string
	}{
		{"empty", stackstatus.Commit{}, ""},
		{"abbreviated", stackstatus.Commit{SHA: headSHA}, "1495bea4"},
		{"dirty", stackstatus.Commit{SHA: headSHA, Dirty: true}, "1495bea4*"},
		{"already short", stackstatus.Commit{SHA: "abc"}, "abc"},
	} {
		assert.Equal(t, tc.want, tc.commit.Short(), tc.name)
	}
}

func TestAgoAndWhen(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		then time.Time
		ago  string
		when string
	}{
		{now.Add(-30 * time.Second), "30s ago", "11:59 · 30s ago"},
		{now.Add(-5 * time.Minute), "5m ago", "11:55 · 5m ago"},
		{now.Add(-3 * time.Hour), "3h ago", "09:00 · 3h ago"},
		{now.Add(-50 * time.Hour), "2d ago", "Sep 27 10:00 · 2d ago"},
	} {
		assert.Equal(t, tc.ago, stackstatus.Ago(now, tc.then))
		assert.Equal(t, tc.when, stackstatus.When(now, tc.then))
	}
}

func TestTook(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "1m5s", stackstatus.Took(now, now.Add(65*time.Second+300*time.Millisecond)))
	assert.Equal(t, "—", stackstatus.Took(now, time.Time{}), "still running")
	assert.Equal(t, "—", stackstatus.Took(now, now.Add(-time.Second)), "clock skew")
}

func TestChangeSummary(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "—", stackstatus.ChangeSummary(nil))
	assert.Equal(t, "+2 ~1 ±1 -3 =20 discard 1 read 4", stackstatus.ChangeSummary(map[string]int{
		stackstatus.ChangeSame: 20, stackstatus.ChangeDelete: 3, stackstatus.ChangeCreate: 2,
		stackstatus.ChangeUpdate: 1, stackstatus.ChangeReplace: 1, "read": 4, "discard": 1,
		"zero": 0,
	}))
}

func TestStateProblems(t *testing.T) {
	t.Parallel()

	assert.Empty(t, stackstatus.State{}.Problems())
	assert.Equal(t, []string{
		"2 pending operation(s) — an update was interrupted",
		"1 awaiting deletion",
		"3 awaiting replacement",
		"1 tainted",
		"4 with init errors",
	}, stackstatus.State{
		PendingOperations: 2, PendingDeletion: 1, PendingReplacement: 3, Tainted: 1, InitErrors: 4,
	}.Problems())
}

func TestNotes(t *testing.T) {
	t.Parallel()

	withRef := func(ref string) stackstatus.Project {
		p := healthy(ingress)
		p.ClusterRef = ref

		return p
	}

	staleConsumer := healthy(ingress)
	staleConsumer.Contract = intPtr(contract - 1)

	stuck := healthy(ingress)
	stuck.State.PendingOperations = 1

	clusterBehind := stackstatus.Project{Name: cluster, IsCluster: true, HasStack: true, Contract: intPtr(contract - 1)}
	clusterUnversioned := stackstatus.Project{Name: cluster, IsCluster: true, HasStack: true}
	clusterCurrent := stackstatus.Project{Name: cluster, IsCluster: true, HasStack: true, Contract: intPtr(contract)}

	for _, tc := range []struct {
		name    string
		project stackstatus.Project
		want    []string
	}{
		{"healthy consumer", healthy(ingress), nil},
		{"no stack says nothing", stackstatus.Project{Name: ingress}, nil},
		{"unreadable says nothing", stackstatus.Project{Name: ingress, HasStack: true, Err: errUnreadable}, nil},
		{"ref unset", withRef(""), []string{"clusterStackRef is not set"}},
		{"ref to another environment", withRef("acme/hetzner-cluster/prod"), []string{"references acme/hetzner-cluster/prod"}},
		{"ref to another project", withRef("acme/other/" + env), []string{"references acme/other/" + env}},
		{"contract skew", staleConsumer, []string{"applied against contract v2, the cluster publishes v3"}},
		{"interrupted update", stuck, []string{"1 pending operation(s) — an update was interrupted"}},
		{"cluster current", clusterCurrent, nil},
		{"cluster behind", clusterBehind, []string{"publishes contract v2, this checkout reads v3 — apply the cluster tier"}},
		{"cluster unversioned", clusterUnversioned, []string{"publishes contract v0, this checkout reads v3 — apply the cluster tier"}},
	} {
		assert.Equal(t, tc.want, stackstatus.Notes(tc.project, header()), tc.name)
	}
}

// TestNotes_WithoutAClusterRowChecksNoRef keeps a report run without the
// cluster tier from calling every reference wrong.
func TestNotes_WithoutAClusterRowChecksNoRef(t *testing.T) {
	t.Parallel()

	h := header()
	h.Cluster = stackstatus.Cluster{}

	p := healthy(ingress)
	p.ClusterRef = "acme/anything/else"
	p.Contract = intPtr(1)

	assert.Empty(t, stackstatus.Notes(p, h))
}

func TestRenderEveryRowShape(t *testing.T) {
	t.Parallel()

	failed := succeeded(headSHA, nil)
	failed.Result = stackstatus.ResultFailed

	misreferenced := healthy("20-network-policy")
	misreferenced.ClusterRef = "acme/hetzner-cluster/prod"

	out := render(t, header(), []stackstatus.Project{
		{
			Name: cluster, IsCluster: true, HasStack: true, Resources: intPtr(26), Contract: intPtr(contract),
			Last: succeeded(headSHA, map[string]int{stackstatus.ChangeSame: 25, stackstatus.ChangeCreate: 1}),
		},
		{Name: "backup", HasStack: true, Resources: intPtr(3), Last: failed, ClusterRef: goodRef},
		healthy("10-node-platform"),
		misreferenced,
		{Name: "30-cluster-services", HasStack: true, InProgress: true, Resources: intPtr(5), Last: succeeded(headSHA, nil), ClusterRef: goodRef},
		{Name: ingress},
		{Name: "50-gitops", HasStack: true, Resources: intPtr(0), ClusterRef: goodRef},
		{Name: "60-broken", HasStack: true, Err: errUnreadable},
	})

	for _, want := range []string{
		"◉ hetzner-iac · platform:status · stack dev\n",
		"cluster   platform-dev · hel1 · https://203.0.113.4:6443",
		"contract  v3 published · this checkout reads v3  ✔",
		"backend   https://api.pulumi.com as oleg",
		"HEAD      1495bea4 (main)",
		"cluster              ✔  update #42",
		"+1 =25",
		"backup               ✖  update #42",
		"10-node-platform     ✔  update #42",
		"20-network-policy    ▲  update #42",
		"references acme/hetzner-cluster/prod",
		"30-cluster-services  ◉  update #42",
		"40-ingress           ○  no stack",
		"50-gitops            ○  never run",
		"60-broken            ✖  unreadable: could not export: 403 forbidden",
		// The worst row's mark leads the verdict.
		"  ✖ 8 stacks · 3 succeeded · 1 failed · 1 running · 3 without a run · 1 need attention · 42 resources · all applied from HEAD\n",
	} {
		assert.Contains(t, out, want)
	}

	assert.NotContains(t, out, "second line", "a multi-line error is cut to its first line")
}

func TestRenderAlignsColumns(t *testing.T) {
	t.Parallel()

	long := healthy("10-node-platform")
	long.ClusterRef = "acme/hetzner-cluster/prod-with-a-long-name"

	out := render(t, header(), []stackstatus.Project{healthy(cluster), long, healthy(ingress)})

	var commitAt []int

	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "update #") {
			commitAt = append(commitAt, strings.Index(line, "1495bea4"))
		}
	}

	require.Len(t, commitAt, 3, out)
	assert.Positive(t, commitAt[0], out)
	assert.Equal(t, commitAt[0], commitAt[1], "a long note must not move the COMMIT column:\n%s", out)
	assert.Equal(t, commitAt[0], commitAt[2], out)
}

func TestRenderFlagsCommitDrift(t *testing.T) {
	t.Parallel()

	dirty := healthy(ingress)
	dirty.Last = succeeded(headSHA, nil)
	dirty.Last.Commit.Dirty = true

	old := healthy("10-node-platform")
	old.Last = succeeded(otherSHA, nil)

	out := render(t, header(), []stackstatus.Project{old, dirty})

	assert.Contains(t, out, "6963f05f")
	assert.Contains(t, out, "1495bea4*")
	assert.Contains(t, out, "applied from 2 commit(s), HEAD is 1495bea4")
}

func TestRenderFlagsAClusterBehindTheCheckout(t *testing.T) {
	t.Parallel()

	h := header()
	h.Cluster.Contract = intPtr(contract - 1)

	assert.Contains(t, render(t, h, nil), "v2 published · this checkout reads v3  ▲ behind")

	h.Cluster = stackstatus.Cluster{}
	out := render(t, h, nil)

	assert.Contains(t, out, "cluster   —")
	assert.Contains(t, out, "contract  — · this checkout reads v3")
}

func TestRenderShowsTheConsoleOnlyWhenKnown(t *testing.T) {
	t.Parallel()

	assert.NotContains(t, render(t, header(), nil), "console")

	h := header()
	h.Cluster.Console = "https://app.pulumi.com/acme/hetzner-cluster/dev"

	assert.Contains(t, render(t, h, nil), "console   https://app.pulumi.com/acme/hetzner-cluster/dev")
}

// TestRenderOrdersTheHeader pins the header's order: where the cluster is and
// who reads it, then the tooling and the tree, then whether they agree.
func TestRenderOrdersTheHeader(t *testing.T) {
	t.Parallel()

	labels := func(h stackstatus.Header) []string {
		var found []string

		for line := range strings.SplitSeq(render(t, h, nil), "\n") {
			label, _, ok := strings.Cut(strings.TrimSpace(line), " ")
			if !ok || !strings.HasPrefix(line, "  ") {
				continue
			}

			if label == "STACK" {
				break
			}

			found = append(found, label)
		}

		return found
	}

	h := header()
	h.Cluster.Console = "https://app.pulumi.com/acme/hetzner-cluster/dev"

	assert.Equal(t, []string{"cluster", "backend", "console", "pulumi", "HEAD", "contract"}, labels(h))

	h.Cluster.Console = ""
	assert.Equal(t, []string{"cluster", "backend", "pulumi", "HEAD", "contract"}, labels(h),
		"an unknown console leaves no gap")
}

func TestRenderColoursOnlyWhenAsked(t *testing.T) {
	t.Parallel()

	projects := []stackstatus.Project{healthy(ingress)}

	assert.NotContains(t, render(t, header(), projects), "\x1b[", "Plain output carries an escape sequence")

	var out strings.Builder

	require.NoError(t, stackstatus.Render(&out, header(), projects, now, report.ANSI))
	assert.Contains(t, out.String(), report.ANSI(report.Green, report.MarkOK))
}

// ansiEscape matches a colour escape, to measure a painted line as it shows.
var ansiEscape = regexp.MustCompile("\x1b\\[[0-9;]*m")

func TestHeaderValuesLineUpWhenPainted(t *testing.T) {
	t.Parallel()

	var out strings.Builder

	require.NoError(t, stackstatus.Render(&out, header(), nil, now, report.ANSI))

	visible := ansiEscape.ReplaceAllString(out.String(), "")

	var starts []int

	for _, value := range []string{"platform-dev", "v3 published", "https://api.pulumi.com", "3.265.0", "1495bea4 (main)"} {
		for line := range strings.SplitSeq(visible, "\n") {
			if i := strings.Index(line, value); i >= 0 && !strings.Contains(line, report.MarkRunning) {
				starts = append(starts, utf8.RuneCountInString(line[:i]))

				break
			}
		}
	}

	require.Len(t, starts, 5, visible)
	assert.Equal(t, slices.Min(starts), slices.Max(starts), "header values start at columns %v:\n%s", starts, visible)
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("terminal went away") }

func TestRenderReportsAFailedWrite(t *testing.T) {
	t.Parallel()

	err := stackstatus.Render(failingWriter{}, header(), nil, now, report.Plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write status report")
}

// TestRenderPaintsNoTextGrey keeps every value readable: grey on a dark,
// low-contrast palette all but vanishes, which is how the header labels, the
// column headings and the WHEN and TOOK columns went unreadable.
func TestRenderPaintsNoTextGrey(t *testing.T) {
	t.Parallel()

	var grey []string

	recording := func(code, s string) string {
		if code == report.Grey {
			grey = append(grey, s)
		}

		return s
	}

	h := header()
	h.Cluster.Console = "https://app.pulumi.com/acme/hetzner-cluster/dev"

	var out strings.Builder

	require.NoError(t, stackstatus.Render(&out, h, []stackstatus.Project{
		healthy(ingress),
		{Name: "backup"},
		{Name: "50-gitops", HasStack: true, ClusterRef: goodRef},
	}, now, recording))

	require.NotEmpty(t, grey, "no row printed the none mark, so this proved nothing")

	for _, painted := range grey {
		assert.Equal(t, report.MarkNone, painted, "text painted grey")
	}
}

const serverURN = "urn:pulumi:dev::cluster::hcloud:index/server:Server::cp-3"

func TestRecovery(t *testing.T) {
	t.Parallel()

	project := stackstatus.Project{Name: "cluster", Dir: "infra/cluster", HasStack: true}

	assert.Empty(t, stackstatus.Recovery(project, "dev"), "nothing interrupted, nothing to say")

	project.State = stackstatus.State{PendingOperations: 2, PendingCreates: []string{serverURN}}
	text := strings.Join(stackstatus.Recovery(project, "dev"), "\n")

	assert.Contains(t, text, "recover cluster — 2 pending operation(s)")
	assert.Contains(t, text, serverURN, "each create is named, so it can be looked up")
	assert.Contains(t, text, "pulumi -C infra/cluster --stack dev refresh --import-pending-creates <urn> <id>")
	assert.Contains(t, text, "pulumi -C infra/cluster --stack dev refresh --clear-pending-creates")
	assert.Contains(t, text, "1 interrupted update(s) or delete(s) clear on: pulumi -C infra/cluster --stack dev refresh")

	project.State = stackstatus.State{PendingOperations: 1}
	text = strings.Join(stackstatus.Recovery(project, "dev"), "\n")

	assert.NotContains(t, text, "--clear-pending-creates",
		"clearing is offered only for creates: on anything else it is the wrong command")
	assert.Contains(t, text, "refresh")

	project.Err = errors.New("unreadable")
	assert.Empty(t, stackstatus.Recovery(project, "dev"), "an unreadable stack has no state to recover from")
}

func TestRender_ShowsRecoveryUnderTheTable(t *testing.T) {
	t.Parallel()

	var out strings.Builder

	projects := []stackstatus.Project{{
		Name: "cluster", Dir: "infra/cluster", IsCluster: true, HasStack: true,
		State: stackstatus.State{PendingOperations: 1, PendingCreates: []string{serverURN}},
	}}

	require.NoError(t, stackstatus.Render(&out, stackstatus.Header{Stack: "dev"}, projects, time.Now(), report.Plain))
	assert.Contains(t, out.String(), "recover cluster")
	assert.Contains(t, out.String(), serverURN)
}

// TestRender_TheVerdictCarriesTheWorstMark: the closing line opens with the
// mark of the worst row, so a glance at the last line says whether to read up.
func TestRender_TheVerdictCarriesTheWorstMark(t *testing.T) {
	t.Parallel()

	attention := healthy("10-node-platform")
	attention.ClusterRef = ""

	for _, tc := range []struct {
		name     string
		projects []stackstatus.Project
		mark     string
	}{
		{"all healthy", []stackstatus.Project{healthy("cluster")}, report.MarkOK},
		{"one needs attention", []stackstatus.Project{healthy("cluster"), attention}, report.MarkWarning},
		{"one without a stack", []stackstatus.Project{healthy("cluster"), {Name: "40-ingress"}}, report.MarkWarning},
		{"one failed", []stackstatus.Project{healthy("cluster"), {
			Name: "backup", HasStack: true, Last: &stackstatus.Update{Result: stackstatus.ResultFailed},
		}}, report.MarkFailed},
	} {
		out := strings.TrimRight(render(t, header(), tc.projects), "\n")
		last := out[strings.LastIndex(out, "\n")+1:]

		assert.True(t, strings.HasPrefix(last, "  "+tc.mark+" "), "%s: %q", tc.name, last)
	}
}
