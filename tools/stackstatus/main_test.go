package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterref"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/stackstatus"
)

const (
	stack       = "dev"
	clusterName = "platform-dev"
	stackURN    = resource.URN("urn:pulumi:dev::hetzner-cluster::pulumi:pulumi:Stack::hetzner-cluster-dev")
)

func TestFindStack(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		summaries   []auto.StackSummary
		wantFound   string
		wantCurrent string
	}{
		{"absent", []auto.StackSummary{{Name: "prod", Current: true}}, "", "prod"},
		{"bare name", []auto.StackSummary{{Name: stack}}, stack, ""},
		{"organisation-qualified", []auto.StackSummary{{Name: "acme/" + stack}}, "acme/" + stack, ""},
		{"a suffix is not a match", []auto.StackSummary{{Name: "predev"}}, "", ""},
		{"selected elsewhere", []auto.StackSummary{{Name: stack}, {Name: "prod", Current: true}}, stack, "prod"},
	} {
		found, current := findStack(tc.summaries, stack)

		if tc.wantFound == "" {
			assert.Nil(t, found, tc.name)
		} else if assert.NotNil(t, found, tc.name) {
			assert.Equal(t, tc.wantFound, found.Name, tc.name)
		}

		assert.Equal(t, tc.wantCurrent, current, tc.name)
	}
}

func untyped(t *testing.T, version int, deployment apitype.DeploymentV3) apitype.UntypedDeployment {
	t.Helper()

	raw, err := json.Marshal(deployment)
	require.NoError(t, err)

	return apitype.UntypedDeployment{Version: version, Deployment: raw}
}

func TestDecodeDeployment(t *testing.T) {
	t.Parallel()

	want := apitype.DeploymentV3{Resources: []apitype.ResourceV3{{URN: stackURN, Type: resource.RootStackType}}}

	got, err := decodeDeployment(untyped(t, apitype.DeploymentSchemaVersionCurrent, want))
	require.NoError(t, err)
	assert.Equal(t, want.Resources[0].URN, got.Resources[0].URN)

	_, err = decodeDeployment(untyped(t, apitype.DeploymentSchemaVersionLatest+1, want))
	require.Error(t, err, "a schema this was not written for is refused, not misread")
	assert.Contains(t, err.Error(), "checkpoint schema")

	_, err = decodeDeployment(apitype.UntypedDeployment{
		Version: apitype.DeploymentSchemaVersionCurrent, Deployment: json.RawMessage(`{"resources": 7}`),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode checkpoint")
}

// The engine writes v4 as soon as a stack uses a feature that needs it — the
// cluster tier did, through ReplaceWith — and the report must still read it.
func TestDecodeDeployment_ReadsAV4CheckpointWithKnownFeatures(t *testing.T) {
	t.Parallel()

	want := apitype.DeploymentV3{Resources: []apitype.ResourceV3{{URN: stackURN, Type: resource.RootStackType}}}

	v4 := untyped(t, apitype.DeploymentSchemaVersionLatest, want)
	v4.Features = []string{"replaceWith"}

	got, err := decodeDeployment(v4)
	require.NoError(t, err)
	assert.Equal(t, want.Resources[0].URN, got.Resources[0].URN)

	v4.Features = []string{"replaceWith", "somethingNew"}

	_, err = decodeDeployment(v4)
	require.Error(t, err, "a feature nobody checked this report against is refused")
	assert.Contains(t, err.Error(), "somethingNew")
	assert.NotContains(t, err.Error(), "replaceWith", "only the unknown ones are named")
}

func TestStateOf(t *testing.T) {
	t.Parallel()

	state := stateOf(apitype.DeploymentV3{
		PendingOperations: []apitype.OperationV2{
			{Type: apitype.OperationTypeCreating, Resource: apitype.ResourceV3{URN: "urn:pulumi:dev::cluster::hcloud:index/server:Server::cp-3"}},
			{Type: apitype.OperationTypeDeleting},
		},
		Resources: []apitype.ResourceV3{
			{Type: resource.RootStackType},
			{Delete: true},
			{PendingReplacement: true, Taint: true},
			{InitErrors: []string{"helm release failed to become ready"}},
			{Protect: true},
		},
	})

	assert.Equal(t, stackstatus.State{
		PendingOperations: 2, PendingCreates: []string{"urn:pulumi:dev::cluster::hcloud:index/server:Server::cp-3"},
		PendingDeletion: 1, PendingReplacement: 1, Tainted: 1, InitErrors: 1,
	}, state)
	assert.Equal(t, stackstatus.State{}, stateOf(apitype.DeploymentV3{}))
}

func TestRootOutputs(t *testing.T) {
	t.Parallel()

	outputs := map[string]any{clusterref.OutputClusterName: clusterName}

	deployment := apitype.DeploymentV3{Resources: []apitype.ResourceV3{
		// A component can register a type named like the root and still not
		// be it: the root is the one with no parent.
		{Type: resource.RootStackType, Parent: stackURN, Outputs: map[string]any{"nested": true}},
		{Type: resource.RootStackType, Outputs: outputs},
	}}

	assert.Equal(t, outputs, rootOutputs(deployment))
	assert.Nil(t, rootOutputs(apitype.DeploymentV3{}))
}

// secretEnvelope is how ExportStack returns a secret output: decrypted,
// because it always passes --show-secrets, but still in its signed map.
func secretEnvelope() map[string]any {
	return map[string]any{resource.SigKey: resource.SecretSig, "plaintext": `"https://203.0.113.4:6443"`}
}

func TestOutputReaders(t *testing.T) {
	t.Parallel()

	outputs := map[string]any{
		clusterref.OutputContractVersion: float64(3),
		clusterref.OutputClusterName:     clusterName,
		clusterref.OutputEndpoint:        secretEnvelope(),
		clusterref.OutputLocation:        float64(1),
	}

	if contract := intOutput(outputs, clusterref.OutputContractVersion); assert.NotNil(t, contract) {
		assert.Equal(t, 3, *contract)
	}

	assert.Nil(t, intOutput(outputs, clusterref.OutputClusterName), "a string is not a number")
	assert.Nil(t, intOutput(nil, clusterref.OutputContractVersion), "a stack with no outputs")

	assert.Equal(t, clusterName, stringOutput(outputs, clusterref.OutputClusterName))
	assert.Empty(t, stringOutput(outputs, clusterref.OutputEndpoint), "a secret is never printed")
	assert.Empty(t, stringOutput(outputs, clusterref.OutputLocation), "a number is not a string")
}

func TestClusterOf(t *testing.T) {
	t.Parallel()

	contract := 3
	read := reading{
		project: stackstatus.Project{IsCluster: true, Contract: &contract},
		outputs: map[string]any{
			clusterref.OutputClusterName: clusterName,
			clusterref.OutputLocation:    "hel1",
			clusterref.OutputEndpoint:    "https://203.0.113.4:6443",
		},
		projectName: "hetzner-cluster",
		console:     "https://app.pulumi.com/acme/hetzner-cluster/dev",
	}

	assert.Equal(t, stackstatus.Cluster{
		Name: clusterName, Location: "hel1", Endpoint: "https://203.0.113.4:6443",
		Contract: &contract, Stack: "hetzner-cluster/dev", Console: read.console,
	}, clusterOf(read, stack))

	read.projectName = ""
	assert.Empty(t, clusterOf(read, stack).Stack, "no project name, no reference to hold the others to")
}

func TestToUpdate(t *testing.T) {
	t.Parallel()

	end := "2026-09-29T12:00:05Z"
	changes := map[string]int{stackstatus.ChangeSame: 25}

	update := toUpdate(auto.UpdateSummary{
		Version: 42, Kind: "update", Result: string(apitype.SucceededResult),
		StartTime: "2026-09-29T12:00:00Z", EndTime: &end, ResourceChanges: &changes,
		Environment: map[string]string{
			envGitHead: "1495bea4", envGitHeadName: "refs/heads/main", envGitDirty: "true",
		},
	})

	assert.Equal(t, &stackstatus.Update{
		Number: 42, Kind: "update", Result: stackstatus.ResultSucceeded,
		Start:   time.Date(2026, time.September, 29, 12, 0, 0, 0, time.UTC),
		End:     time.Date(2026, time.September, 29, 12, 0, 5, 0, time.UTC),
		Changes: changes,
		Commit:  stackstatus.Commit{SHA: "1495bea4", Branch: "main", Dirty: true},
	}, update)

	running := toUpdate(auto.UpdateSummary{Kind: "update", Result: string(apitype.InProgressResult), StartTime: "garbage"})
	assert.True(t, running.End.IsZero(), "no end time while it runs")
	assert.True(t, running.Start.IsZero(), "an unreadable time is zero")
	assert.Nil(t, running.Changes)
	assert.False(t, running.Commit.Dirty)
}

func TestResult(t *testing.T) {
	t.Parallel()

	for pulumi, want := range map[apitype.UpdateResult]stackstatus.Result{
		apitype.SucceededResult:  stackstatus.ResultSucceeded,
		apitype.FailedResult:     stackstatus.ResultFailed,
		apitype.InProgressResult: stackstatus.ResultInProgress,
		apitype.NotStartedResult: stackstatus.ResultNotStarted,
		"cancelled":              stackstatus.Result("cancelled"),
	} {
		assert.Equal(t, want, result(pulumi))
	}
}

func TestRun_RefusesMissingArguments(t *testing.T) {
	t.Parallel()

	require.ErrorIs(t, run("", []string{"infra/cluster"}), errNoStack)
	require.ErrorIs(t, run(stack, nil), errNoProjects)
}
