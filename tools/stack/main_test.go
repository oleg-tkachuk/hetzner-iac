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

func TestQualifiedName_ReturnsTheReferenceForTheNamedStack(t *testing.T) {
	t.Parallel()

	// The shape `pulumi stack ls -Q --json` returns: every row qualified,
	// while the caller knows only "dev".
	const list = `[{"name":"acme/hetzner-cluster/dev","current":true},{"name":"acme/hetzner-cluster/prod"}]`

	got, err := qualifiedName([]byte(list), "dev")

	require.NoError(t, err)
	assert.Equal(t, "acme/hetzner-cluster/dev", got)
}

func TestQualifiedName_DoesNotMatchOnASubstringOfAnotherStack(t *testing.T) {
	t.Parallel()

	// "dev" must not be answered by "dev-2": the whole point of deriving the
	// reference is that it cannot come back pointing somewhere else.
	const list = `[{"name":"acme/hetzner-cluster/dev-2"},{"name":"acme/hetzner-cluster/staging"}]`

	_, err := qualifiedName([]byte(list), "dev")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "acme/hetzner-cluster/dev-2", "the error lists what does exist")
}

func TestQualifiedName_Errors(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		list  string
		stack string
		want  string
	}{
		// A reference to a stack that is not there is not a thing to write
		// into five layers' config and discover at apply time.
		"absent": {`[{"name":"acme/hetzner-cluster/prod"}]`, "dev", "no stack named"},

		"no stacks at all": {`[]`, "dev", "the project has none"},

		// Reported as absent, a truncated response would send the caller to
		// an explicit ref= for a problem that was a bad response.
		"unparseable": {"not json at all", "dev", "no usable json"},

		// A self-managed backend qualifies nothing, so there is no
		// organization to name and no reference to derive.
		"unqualified": {`[{"name":"dev"}]`, "dev", "pass ref= explicitly"},

		// Two segments is neither shape, and guessing which half is missing
		// is how a wrong reference gets written confidently.
		"half qualified": {`[{"name":"hetzner-cluster/dev"}]`, "dev", "pass ref= explicitly"},
	} {
		_, err := qualifiedName([]byte(tc.list), tc.stack)

		require.Error(t, err, name)
		assert.Contains(t, err.Error(), tc.want, name)
	}
}

func TestRun_RejectsAnUnknownCommand(t *testing.T) {
	t.Parallel()

	err := run(context.Background(), []string{"destroy", "infra/cluster", "dev"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "exists, ensure or ref")
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
