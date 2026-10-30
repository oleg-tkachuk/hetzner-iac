package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optlist"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/talossecrets"
)

var errBoom = errors.New("boom")

const kitStack = "dev"

// fakeStacks is one project's workspace as the backend would answer it.
type fakeStacks struct {
	exported apitype.UntypedDeployment
	outputs  auto.OutputMap
	listed   []auto.StackSummary
	err      error
}

func (f fakeStacks) ExportStack(context.Context, string) (apitype.UntypedDeployment, error) {
	return f.exported, f.err
}

func (f fakeStacks) StackOutputs(context.Context, string) (auto.OutputMap, error) {
	return f.outputs, f.err
}

func (f fakeStacks) ListStacks(context.Context, ...optlist.Option) ([]auto.StackSummary, error) {
	return f.listed, f.err
}

// workspaceOf is a pulumiWorkspace whose every directory opens as stacks.
func workspaceOf(stacks stackReader) pulumiWorkspace {
	return pulumiWorkspace{
		requireLogin: func([]string) error { return nil },
		newWorkspace: func(context.Context, string) (stackReader, error) { return stacks, nil },
	}
}

// withSecrets is an export holding the Talos secrets resource.
func withSecrets(t *testing.T) apitype.UntypedDeployment {
	t.Helper()

	raw, err := json.Marshal(apitype.DeploymentV3{Resources: []apitype.ResourceV3{{
		Type:    talossecrets.SecretsResourceType,
		Outputs: map[string]any{"talosVersion": "v1.13.10"},
	}}})
	require.NoError(t, err)

	return apitype.UntypedDeployment{Version: apitype.DeploymentSchemaVersionCurrent, Deployment: raw}
}

func TestBundle_IsTheSecretsResourceOfTheClusterTier(t *testing.T) {
	t.Parallel()

	bundle, err := workspaceOf(fakeStacks{exported: withSecrets(t)}).Bundle(context.Background(), kitStack)
	require.NoError(t, err)
	assert.JSONEq(t, `{"talosVersion": "v1.13.10"}`, string(bundle))

	_, err = workspaceOf(fakeStacks{exported: apitype.UntypedDeployment{Deployment: json.RawMessage(`{}`)}}).
		Bundle(context.Background(), kitStack)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "holds no "+talossecrets.SecretsResourceType)
}

func TestPulumiWorkspace_NamesWhatItCouldNotRead(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	failing := workspaceOf(fakeStacks{err: errBoom})

	refused := workspaceOf(fakeStacks{})
	refused.requireLogin = func([]string) error { return errBoom }

	unopened := workspaceOf(nil)
	unopened.newWorkspace = func(context.Context, string) (stackReader, error) { return nil, errBoom }

	unencodable := workspaceOf(fakeStacks{exported: apitype.UntypedDeployment{Deployment: json.RawMessage(`{`)}})

	for want, read := range map[string]func() error{
		"boom": func() error { _, err := refused.Stacks(ctx, BackupDir); return err },
		"open " + BackupDir + " as a pulumi workspace: boom": func() error {
			_, err := unopened.Stacks(ctx, BackupDir)
			return err
		},
		"export dev in " + TopologyDir + ": boom": func() error { _, err := failing.Bundle(ctx, kitStack); return err },
		"outputs of dev in " + BackupDir + ": boom": func() error {
			_, err := failing.Outputs(ctx, BackupDir, kitStack)
			return err
		},
		"list stacks in " + BackupDir + ": boom": func() error { _, err := failing.Stacks(ctx, BackupDir); return err },
		"re-encode the export of dev":            func() error { _, err := unencodable.Bundle(ctx, kitStack); return err },
	} {
		err := read()
		require.Error(t, err, want)
		assert.Contains(t, err.Error(), want)
	}
}

func TestPulumiWorkspace_ReadsTheBackupTier(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	backup := workspaceOf(fakeStacks{
		outputs: auto.OutputMap{"resticPassword": {Value: "hunter2", Secret: true}},
		listed:  []auto.StackSummary{{Name: "acme/" + kitStack}},
	})

	outputs, err := backup.Outputs(ctx, BackupDir, kitStack)
	require.NoError(t, err)
	assert.JSONEq(t, `{"resticPassword": "hunter2"}`, string(outputs))

	stacks, err := backup.Stacks(ctx, BackupDir)
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/" + kitStack}, stacks)
}
