package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStackNamed_FindsAndMisses(t *testing.T) {
	t.Parallel()

	// The shape `pulumi stack ls --json` returns.
	const list = `[{"name":"dev","current":true},{"name":"staging"}]`

	for name, tc := range map[string]struct {
		stack string
		want  bool
	}{
		"present":              {"dev", true},
		"present, not current": {"staging", true},
		"absent":               {"prod", false},
		"empty name":           {"", false},
	} {
		got, err := stackNamed([]byte(list), tc.stack)
		require.NoError(t, err, name)
		assert.Equal(t, tc.want, got, name)
	}
}

func TestStackNamed_AnEmptyListIsAbsentNotAnError(t *testing.T) {
	t.Parallel()

	// A project with no stacks yet is the ordinary first-run state.
	got, err := stackNamed([]byte(`[]`), "dev")

	require.NoError(t, err)
	assert.False(t, got)
}

func TestStackNamed_UnparseableIsAnErrorNotAbsent(t *testing.T) {
	t.Parallel()

	// The distinction the jq pipeline could not make. Reported as absent, a
	// broken or truncated response would send init to create a stack that
	// already exists, and the operator would see "stack already exists" for a
	// problem that was a bad response.
	_, err := stackNamed([]byte("not json at all"), "dev")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no usable json")
}

func TestStackAction_PairsTheVerbWithWhatItDid(t *testing.T) {
	t.Parallel()

	verb, state := stackAction(false)
	assert.Equal(t, "init", verb)
	assert.Equal(t, StateCreated, state)

	verb, state = stackAction(true)
	assert.Equal(t, "select", verb)
	assert.Equal(t, StateExisting, state,
		"a stack that was only selected must not be reported as created")
}

func TestRun_RejectsAnUnknownCommand(t *testing.T) {
	t.Parallel()

	err := run(context.Background(), []string{"destroy", "infra/cluster", "dev"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "exists or ensure")
}

func TestRun_RejectsTheWrongNumberOfArguments(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"none":     {},
		"too few":  {"exists", "infra/cluster"},
		"too many": {"exists", "infra/cluster", "dev", "extra"},
	} {
		err := run(context.Background(), args)

		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "usage:", name)
	}
}
