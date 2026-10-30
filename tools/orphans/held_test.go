package main

import (
	"context"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optlist"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeStates is a workspace whose stacks export the given states.
type fakeStates struct {
	states    map[string]apitype.UntypedDeployment
	order     []string
	listErr   error
	exportErr error
}

func (f fakeStates) ListStacks(context.Context, ...optlist.Option) ([]auto.StackSummary, error) {
	summaries := make([]auto.StackSummary, 0, len(f.order))
	for _, name := range f.order {
		summaries = append(summaries, auto.StackSummary{Name: name})
	}

	return summaries, f.listErr
}

func (f fakeStates) ExportStack(_ context.Context, name string) (apitype.UntypedDeployment, error) {
	return f.states[name], f.exportErr
}

// openFakes opens the fake workspace of each directory, and refuses any other.
func openFakes(byDir map[string]fakeStates) workspaceOpener {
	return func(_ context.Context, dir string) (stackStates, error) {
		workspace, ok := byDir[dir]
		if !ok {
			return nil, errBoom
		}

		return workspace, nil
	}
}

func TestReadHeld_EveryStackOfEveryDirectory(t *testing.T) {
	t.Parallel()

	current := apitype.DeploymentSchemaVersionCurrent
	open := openFakes(map[string]fakeStates{
		"infra/cluster": {
			order: []string{"dev", "prod"},
			states: map[string]apitype.UntypedDeployment{
				"dev":  deployment(t, current, res(typeServer, "1")),
				"prod": deployment(t, current, res(typeServer, "2")),
			},
		},
		// A directory with no stacks holds nothing: the truth after a teardown.
		"layers/10-node-platform": {},
		"infra/backup": {
			order:  []string{"dev"},
			states: map[string]apitype.UntypedDeployment{"dev": deployment(t, current, res(typeStorageBox, "3"))},
		},
	})

	held, err := readHeld(t.Context(), []string{"infra/cluster", "layers/10-node-platform", "infra/backup"}, open)

	require.NoError(t, err)
	assert.Equal(t, 3, held.Stacks)
	assert.True(t, held.Holds(KindServer, 1))
	assert.True(t, held.Holds(KindServer, 2), "another environment's resource is its stack's")
	assert.True(t, held.Holds(KindStorageBox, 3))
}

func TestReadHeld_Refuses(t *testing.T) {
	t.Parallel()

	current := apitype.DeploymentSchemaVersionCurrent
	one := map[string]apitype.UntypedDeployment{"dev": deployment(t, current)}

	for _, tc := range []struct {
		name, dir string
		workspace fakeStates
		want      string
	}{
		{"an unopenable directory", "elsewhere", fakeStates{}, "elsewhere: boom"},
		{"an unlistable backend", "infra/cluster", fakeStates{listErr: errBoom}, "infra/cluster: list stacks: boom"},
		{
			"an unexportable stack", "infra/cluster",
			fakeStates{order: []string{"dev"}, states: one, exportErr: errBoom},
			"infra/cluster: export dev: boom",
		},
		{
			"an unreadable state", "infra/cluster",
			fakeStates{order: []string{"dev"}, states: map[string]apitype.UntypedDeployment{
				"dev": deployment(t, apitype.DeploymentSchemaVersionLatest+1),
			}},
			"infra/cluster: dev: state schema version",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := readHeld(t.Context(), []string{tc.dir}, openFakes(map[string]fakeStates{
				"infra/cluster": tc.workspace,
			}))

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestAddDeployment_ARecordSetWithNoZoneClaimsNone(t *testing.T) {
	t.Parallel()

	blank := res(typeZoneRrset, "a")
	blank.Inputs = map[string]any{zoneInput: ""}

	unnamed := res(typeZoneRrset, "b")

	held := NewHeld()
	require.NoError(t, addDeployment(held, deployment(t, apitype.DeploymentSchemaVersionCurrent, blank, unnamed)))
	assert.Empty(t, held.Zones)
	assert.Empty(t, held.IDs, "a record set is not a held kind itself")
}
