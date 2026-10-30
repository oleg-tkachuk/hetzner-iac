package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/events"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optlist"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/report"
)

var errBoom = errors.New("boom")

const (
	policyDir  = "layers/20-network-policy"
	backupDir  = "infra/backup"
	otherStack = "prod"
)

// fakeWorkspace is one project as the backend would answer for it.
type fakeWorkspace struct {
	summaries []auto.StackSummary
	// events are what the refresh preview sends before it closes the stream.
	events []events.EngineEvent

	listErr, selectErr, refreshErr, reselectErr error

	// reselected is every stack SelectStack put back.
	reselected []string
}

func (f *fakeWorkspace) ListStacks(context.Context, ...optlist.Option) ([]auto.StackSummary, error) {
	return f.summaries, f.listErr
}

func (f *fakeWorkspace) SelectStack(_ context.Context, stack string) error {
	f.reselected = append(f.reselected, stack)

	return f.reselectErr
}

func (f *fakeWorkspace) Select(context.Context, string) (refresher, error) {
	if f.selectErr != nil {
		return nil, f.selectErr
	}

	return f, nil
}

// PreviewRefresh closes the stream on every path, as the SDK does: collect
// reads until it closes.
func (f *fakeWorkspace) PreviewRefresh(_ context.Context, stream chan<- events.EngineEvent) error {
	defer close(stream)

	for _, event := range f.events {
		stream <- event
	}

	return f.refreshErr
}

func opener(projects map[string]*fakeWorkspace) workspaceOpener {
	return func(_ context.Context, dir string) (projectWorkspace, error) {
		if fake, ok := projects[dir]; ok {
			return fake, nil
		}

		return nil, errBoom
	}
}

func testDeps(open workspaceOpener, out *bytes.Buffer) deps {
	return deps{
		requireLogin: func([]string) error { return nil },
		open:         open,
		out:          out,
		paint:        report.Plain,
	}
}

// drifted is a project whose policy was edited in the cluster, with another
// stack selected in its directory.
func drifted() *fakeWorkspace {
	return &fakeWorkspace{
		summaries: []auto.StackSummary{{Name: stack}, {Name: otherStack, Current: true}},
		events:    []events.EngineEvent{outputs(apitype.OpUpdate, "spec.description"), outputs(apitype.OpSame)},
	}
}

func TestRun_ReportsDriftAndFails(t *testing.T) {
	t.Parallel()

	policy := drifted()
	clean := &fakeWorkspace{summaries: []auto.StackSummary{{Name: stack}}}

	var out bytes.Buffer

	err := run(stack, []string{policyDir, backupDir},
		testDeps(opener(map[string]*fakeWorkspace{policyDir: policy, backupDir: clean}), &out))
	require.ErrorIs(t, err, errDrifted)

	assert.Contains(t, out.String(), "allow-hcloud-metadata")
	assert.Contains(t, out.String(), "spec.description")
	assert.Equal(t, []string{otherStack}, policy.reselected, "the operator's selection is put back")
	assert.Empty(t, clean.reselected)
}

func TestRun_PassesWhenTheCloudAgrees(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	clean := &fakeWorkspace{summaries: []auto.StackSummary{{Name: stack, Current: true}}}

	require.NoError(t, run(stack, []string{backupDir}, testDeps(opener(map[string]*fakeWorkspace{backupDir: clean}), &out)))
	assert.Contains(t, out.String(), "1 stack")
	assert.Empty(t, clean.reselected, "the stack was the selected one already")
}

func TestRun_StopsWithoutALogin(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	d := testDeps(opener(nil), &out)
	d.requireLogin = func([]string) error { return errBoom }

	require.ErrorIs(t, run(stack, []string{backupDir}, d), errBoom)
	assert.Empty(t, out.String())
}

func TestReadProject_PutsEachFailureOnItsRow(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		breaks  func(*fakeWorkspace)
		wantErr string
	}{
		"list":     {func(f *fakeWorkspace) { f.listErr = errBoom }, "boom"},
		"select":   {func(f *fakeWorkspace) { f.selectErr = errBoom }, "select dev: boom"},
		"refresh":  {func(f *fakeWorkspace) { f.refreshErr = errBoom }, "refresh preview of dev: boom"},
		"reselect": {func(f *fakeWorkspace) { f.reselectErr = errBoom }, "refresh preview of dev: reselect prod: boom"},
	} {
		fake := drifted()
		tc.breaks(fake)

		project := readProject(context.Background(), opener(map[string]*fakeWorkspace{policyDir: fake}), policyDir, stack)
		require.Error(t, project.Err, name)
		assert.Equal(t, tc.wantErr, project.Err.Error(), name)
		assert.Empty(t, project.Changes, name)
	}

	project := readProject(context.Background(), opener(nil), policyDir, stack)
	require.ErrorIs(t, project.Err, errBoom, "a directory that is no workspace")
}

func TestReadProject_NoStackIsNoRead(t *testing.T) {
	t.Parallel()

	fake := &fakeWorkspace{summaries: []auto.StackSummary{{Name: otherStack}}, selectErr: errBoom}

	project := readProject(context.Background(), opener(map[string]*fakeWorkspace{policyDir: fake}), policyDir, stack)
	require.NoError(t, project.Err)
	assert.False(t, project.HasStack)
	assert.Equal(t, "20-network-policy", project.Name)
}
