package main

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/opthistory"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optlist"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	pulumiworkspace "github.com/pulumi/pulumi/sdk/v3/go/common/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterref"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/layer"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/report"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/stackstatus"
)

var errBoom = errors.New("boom")

const (
	layerDir     = "layers/10-node-platform"
	clusterRef   = "acme/hetzner-cluster/dev"
	backendURL   = "https://api.pulumi.com"
	backendUser  = "operator"
	cliVersion   = "v3.273.0"
	appliedSHA   = "0123456789abcdef"
	otherStack   = "prod"
	updateNumber = 42
)

// fakeWorkspace is one project as the backend would answer for it.
type fakeWorkspace struct {
	project    string
	summaries  []auto.StackSummary
	deployment apitype.UntypedDeployment
	config     map[string]string
	history    []auto.UpdateSummary

	settingsErr, whoErr, listErr, exportErr, configErr, selectErr, historyErr, reselectErr error

	// reselected is every stack SelectStack put back.
	reselected []string
}

func (f *fakeWorkspace) ProjectSettings(context.Context) (*pulumiworkspace.Project, error) {
	return &pulumiworkspace.Project{Name: tokens.PackageName(f.project)}, f.settingsErr
}

func (f *fakeWorkspace) WhoAmIDetails(context.Context) (auto.WhoAmIResult, error) {
	return auto.WhoAmIResult{URL: backendURL, User: backendUser}, f.whoErr
}

func (f *fakeWorkspace) PulumiVersion() string { return cliVersion }

func (f *fakeWorkspace) ListStacks(context.Context, ...optlist.Option) ([]auto.StackSummary, error) {
	return f.summaries, f.listErr
}

func (f *fakeWorkspace) ExportStack(context.Context, string) (apitype.UntypedDeployment, error) {
	return f.deployment, f.exportErr
}

func (f *fakeWorkspace) GetConfig(_ context.Context, _, key string) (auto.ConfigValue, error) {
	return auto.ConfigValue{Value: f.config[key]}, f.configErr
}

func (f *fakeWorkspace) SelectStack(_ context.Context, stack string) error {
	f.reselected = append(f.reselected, stack)

	return f.reselectErr
}

func (f *fakeWorkspace) Select(context.Context, string) (stackHistory, error) {
	if f.selectErr != nil {
		return nil, f.selectErr
	}

	return f, nil
}

func (f *fakeWorkspace) History(context.Context, int, int, ...opthistory.Option) ([]auto.UpdateSummary, error) {
	return f.history, f.historyErr
}

// opener answers each directory with its fake, and refuses any other.
func opener(projects map[string]*fakeWorkspace) workspaceOpener {
	return func(_ context.Context, dir string) (projectWorkspace, error) {
		if fake, ok := projects[dir]; ok {
			return fake, nil
		}

		return nil, errBoom
	}
}

// exported is a checkpoint whose root holds outputs.
func exported(t *testing.T, outputs map[string]any) apitype.UntypedDeployment {
	t.Helper()

	return untyped(t, apitype.DeploymentSchemaVersionCurrent, apitype.DeploymentV3{
		Resources: []apitype.ResourceV3{{URN: stackURN, Type: resource.RootStackType, Outputs: outputs}},
	})
}

func succeeded() []auto.UpdateSummary {
	return []auto.UpdateSummary{{
		Version:     updateNumber,
		Kind:        "update",
		Result:      string(apitype.SucceededResult),
		StartTime:   "2026-10-30T10:00:00Z",
		Environment: map[string]string{envGitHead: appliedSHA},
	}}
}

// clusterTier is the cluster tier's project, applied, with the layer's stack
// selected elsewhere.
func clusterTier(t *testing.T) *fakeWorkspace {
	t.Helper()

	return &fakeWorkspace{
		project:   "hetzner-cluster",
		summaries: []auto.StackSummary{{Name: stack, ResourceCount: new(17)}},
		deployment: exported(t, map[string]any{
			clusterref.OutputClusterName:     clusterName,
			clusterref.OutputContractVersion: float64(clusterref.ContractVersion),
		}),
		history: succeeded(),
	}
}

// consumer is a layer applied against the cluster, whose directory has
// another stack selected.
func consumer(t *testing.T) *fakeWorkspace {
	t.Helper()

	return &fakeWorkspace{
		project:    "node-platform",
		summaries:  []auto.StackSummary{{Name: stack}, {Name: otherStack, Current: true}},
		deployment: exported(t, nil),
		config:     map[string]string{layer.ClusterStackRefKey: clusterRef},
		history:    succeeded(),
	}
}

func testDeps(open workspaceOpener, out *bytes.Buffer) deps {
	return deps{
		requireLogin: func([]string) error { return nil },
		open:         open,
		head:         func(context.Context) stackstatus.Commit { return stackstatus.Commit{SHA: appliedSHA} },
		now:          func() time.Time { return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC) },
		out:          out,
		paint:        report.Plain,
	}
}

func TestRun_ReportsEveryProjectAndTheClusterTheyShare(t *testing.T) {
	t.Parallel()

	cluster, layered := clusterTier(t), consumer(t)

	var out bytes.Buffer

	err := run(stack, []string{clusterspec.ClusterDir, layerDir},
		testDeps(opener(map[string]*fakeWorkspace{clusterspec.ClusterDir: cluster, layerDir: layered}), &out))
	require.NoError(t, err)

	rendered := out.String()
	for _, want := range []string{
		"cluster   " + clusterName, backendURL + " as " + backendUser, cliVersion,
		"cluster           ✔  update #42", "10-node-platform  ✔  update #42",
		"2 stacks · 2 succeeded · 17 resources",
	} {
		assert.Contains(t, rendered, want)
	}

	assert.Equal(t, []string{otherStack}, layered.reselected, "the operator's selection is put back")
	assert.Empty(t, cluster.reselected, "nothing was selected before, so nothing is put back")
}

func TestRun_StopsBeforeReadingWhatItCannotReach(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	login := testDeps(opener(nil), &out)
	login.requireLogin = func([]string) error { return errBoom }
	require.ErrorIs(t, run(stack, []string{layerDir}, login), errBoom)

	err := run(stack, []string{layerDir}, testDeps(opener(nil), &out))
	require.ErrorIs(t, err, errBoom)
	assert.Contains(t, err.Error(), "open "+layerDir+" as a pulumi workspace")

	err = run(stack, []string{layerDir},
		testDeps(opener(map[string]*fakeWorkspace{layerDir: {whoErr: errBoom}}), &out))
	require.ErrorIs(t, err, errBoom)
	assert.Contains(t, err.Error(), "ask the backend who this is")

	assert.Empty(t, out.String(), "no report is printed for a run that failed")
}

func TestReadProject_PutsEachFailureOnItsRow(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		breaks  func(*fakeWorkspace)
		wantErr string
	}{
		"list":     {func(f *fakeWorkspace) { f.listErr = errBoom }, "boom"},
		"export":   {func(f *fakeWorkspace) { f.exportErr = errBoom }, "export dev: boom"},
		"select":   {func(f *fakeWorkspace) { f.selectErr = errBoom }, "select dev: boom"},
		"history":  {func(f *fakeWorkspace) { f.historyErr = errBoom }, "history of dev: boom"},
		"reselect": {func(f *fakeWorkspace) { f.reselectErr = errBoom }, "history of dev: reselect prod: boom"},
	} {
		fake := consumer(t)
		tc.breaks(fake)

		read := readProject(context.Background(), opener(map[string]*fakeWorkspace{layerDir: fake}), layerDir, stack)
		require.Error(t, read.project.Err, name)
		assert.Equal(t, tc.wantErr, read.project.Err.Error(), name)
	}

	read := readProject(context.Background(), opener(nil), layerDir, stack)
	require.ErrorIs(t, read.project.Err, errBoom, "a directory that is no workspace")
}

func TestReadProject_ReadsALayersRow(t *testing.T) {
	t.Parallel()

	read := readProject(context.Background(), opener(map[string]*fakeWorkspace{layerDir: consumer(t)}), layerDir, stack)
	require.NoError(t, read.project.Err)

	assert.True(t, read.project.HasStack)
	assert.False(t, read.project.IsCluster)
	assert.Equal(t, clusterRef, read.project.ClusterRef)
	assert.Equal(t, "node-platform", read.projectName)
	require.NotNil(t, read.project.Last)
	assert.Equal(t, updateNumber, read.project.Last.Number)
	assert.Equal(t, appliedSHA, read.project.Last.Commit.SHA)
}

func TestReadProject_ReadsWhatItCanAndLeavesTheRestBlank(t *testing.T) {
	t.Parallel()

	// An unreadable project name or reference is not a failure: the row says
	// it is not set to anything this report can check.
	fake := consumer(t)
	fake.settingsErr, fake.configErr, fake.history = errBoom, errBoom, nil

	read := readProject(context.Background(), opener(map[string]*fakeWorkspace{layerDir: fake}), layerDir, stack)
	require.NoError(t, read.project.Err)
	assert.Empty(t, read.projectName)
	assert.Empty(t, read.project.ClusterRef)
	assert.Nil(t, read.project.Last, "a stack never run has no last update")

	absent := consumer(t)
	absent.summaries = []auto.StackSummary{{Name: otherStack}}

	read = readProject(context.Background(), opener(map[string]*fakeWorkspace{layerDir: absent}), layerDir, stack)
	require.NoError(t, read.project.Err)
	assert.False(t, read.project.HasStack)
}
