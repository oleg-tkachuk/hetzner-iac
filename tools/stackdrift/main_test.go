package main

import (
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/events"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/stackdrift"
)

const (
	stack     = "dev"
	policyURN = "urn:pulumi:dev::network-policy::kubernetes:yaml:ConfigGroup$" +
		"kubernetes:cilium.io/v2:CiliumClusterwideNetworkPolicy::allow-hcloud-metadata"
)

// outputs is a refresh preview's outputs event for the policy, as the engine
// sends it: measured on dev after patching spec.description with kubectl.
func outputs(op apitype.OpType, fields ...string) events.EngineEvent {
	diff := map[string]apitype.PropertyDiff{}
	for _, field := range fields {
		diff[field] = apitype.PropertyDiff{Kind: apitype.DiffUpdate}
	}

	return events.EngineEvent{EngineEvent: apitype.EngineEvent{
		ResOutputsEvent: &apitype.ResOutputsEvent{Metadata: apitype.StepEventMetadata{
			Op: op, URN: policyURN, DetailedDiff: diff,
		}},
	}}
}

func TestToChange(t *testing.T) {
	t.Parallel()

	change, ok := toChange(outputs(apitype.OpUpdate, "spec.egress", "spec.description").ResOutputsEvent.Metadata)
	require.True(t, ok)
	assert.Equal(t, stackdrift.Change{
		Type: "kubernetes:cilium.io/v2:CiliumClusterwideNetworkPolicy", Name: "allow-hcloud-metadata",
		Op: stackdrift.OpUpdate, Fields: []string{"spec.description", "spec.egress"},
	}, change, "the type is the resource's own, not its parent's, and fields are sorted")

	gone, ok := toChange(outputs(apitype.OpDelete).ResOutputsEvent.Metadata)
	require.True(t, ok)
	assert.Equal(t, stackdrift.OpDelete, gone.Op)

	reference := outputs(apitype.OpUpdate).ResOutputsEvent.Metadata
	reference.URN = "urn:pulumi:dev::backup::pulumi:pulumi:StackReference::oleg-tkachuk/hetzner-cluster/dev"
	_, ok = toChange(reference)
	assert.False(t, ok, "a StackReference reads another stack, not the cloud")

	for _, op := range []apitype.OpType{apitype.OpSame, apitype.OpRefresh, apitype.OpRead, apitype.OpCreate} {
		_, ok := toChange(outputs(op).ResOutputsEvent.Metadata)
		assert.False(t, ok, "%s is not drift", op)
	}
}

func TestCollect_ReadsUntilTheStreamCloses(t *testing.T) {
	t.Parallel()

	sent := []events.EngineEvent{
		outputs(apitype.OpSame),
		{EngineEvent: apitype.EngineEvent{ResourcePreEvent: &apitype.ResourcePreEvent{
			Metadata: apitype.StepEventMetadata{Op: apitype.OpRefresh, URN: policyURN},
		}}},
		outputs(apitype.OpUpdate, "spec.description"),
	}

	stream := make(chan events.EngineEvent, len(sent))
	for _, event := range sent {
		stream <- event
	}

	close(stream)

	changes := collect(stream)
	require.Len(t, changes, 1, "a pre-event says what will be refreshed, not what changed")
	assert.Equal(t, []string{"spec.description"}, changes[0].Fields)
}

func TestFindStack(t *testing.T) {
	t.Parallel()

	found, current := findStack([]auto.StackSummary{{Name: "acme/" + stack}, {Name: "prod", Current: true}}, stack)
	assert.Equal(t, "acme/"+stack, found, "Pulumi Cloud lists the organisation too")
	assert.Equal(t, "prod", current)

	found, _ = findStack([]auto.StackSummary{{Name: "predev"}}, stack)
	assert.Empty(t, found, "a suffix of a longer name is another stack")
}

func TestRun_RefusesMissingArguments(t *testing.T) {
	t.Parallel()

	require.ErrorIs(t, run("", []string{"infra/cluster"}), errNoStack)
	require.ErrorIs(t, run(stack, nil), errNoProjects)
}
