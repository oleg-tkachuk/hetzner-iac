package main

import (
	"context"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/hetzner"
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
	assert.Contains(t, err.Error(), "exists, ensure, ref or list")
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

// sparse is a topology that states only what has no default, which is the
// shape every committed one has. Loading it is what fills in the
// architecture and the server types.
const sparse = `
apiVersion: hetzner-iac/v1
kind: Cluster
metadata:
  name: platform-dev
placement:
  location: hel1
network:
  adminCIDRs: [203.0.113.4/32]
talos:
  version: v1.13.10
kubernetes:
  version: v1.36.4
controlPlane:
  count: 3
`

// armWithAPool exercises the other architecture and a worker pool, because
// both change what the node column has to say.
const armWithAPool = `
apiVersion: hetzner-iac/v1
kind: Cluster
metadata:
  name: platform-arm
placement:
  location: fsn1
network:
  adminCIDRs: [203.0.113.4/32]
talos:
  version: v1.13.10
  architecture: arm
kubernetes:
  version: v1.36.4
controlPlane:
  count: 1
workerPools:
  - name: general
    count: 2
`

func loaded(t *testing.T, raw string) topologyFile {
	t.Helper()

	topology, err := hetzner.ParseTopology([]byte(raw))
	require.NoError(t, err)

	return topologyFile{Topology: topology}
}

// TestMerge_IsTheUnionOfBothSources keeps an environment from disappearing
// because one half of it is absent.
//
// The two halves are independent: the backend knows which stacks exist and
// the working copy holds the topologies, and `cluster.<stack>.yaml` is
// gitignored — so a stack whose description lives in somebody else's clone is
// the normal case rather than an error, and a topology for a stack nobody has
// created yet is what a new environment looks like before init.
func TestMerge_IsTheUnionOfBothSources(t *testing.T) {
	t.Parallel()

	listing := []byte(`[
	  {"name": "dev", "lastUpdate": "2026-09-15T13:42:52.000Z", "resourceCount": 24},
	  {"name": "orphan", "resourceCount": 0}
	]`)

	environments, err := merge(listing, map[string]topologyFile{
		"dev":     loaded(t, sparse),
		"planned": loaded(t, armWithAPool),
		"broken":  {Err: assert.AnError},
	})
	require.NoError(t, err)

	names := make([]string, 0, len(environments))
	for _, environment := range environments {
		names = append(names, environment.Name)
	}

	assert.Equal(t, []string{"broken", "dev", "orphan", "planned"}, names,
		"sorted by name, and every environment from either source is present")

	byName := map[string]Environment{}
	for _, environment := range environments {
		byName[environment.Name] = environment
	}

	assert.Equal(t, "yes", state(byName["dev"]), "both halves")
	assert.Equal(t, 24, byName["dev"].Resources)

	assert.Equal(t, "missing", state(byName["orphan"]),
		"a stack with no topology in this working copy")
	assert.Equal(t, "no stack", state(byName["planned"]),
		"a topology for a stack that has not been created")
	assert.Equal(t, "invalid", state(byName["broken"]),
		"a topology that is there and unreadable is not the same as one that is absent")
}

func TestMerge_UnparseableListingIsAnError(t *testing.T) {
	t.Parallel()

	_, err := merge([]byte("not json"), nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no usable json")
}

// TestTable_LeavesNoCellBlank pins the marker rather than the widths: a blank
// cell reads as a value of nothing, and every one of these means "not knowable
// from here".
func TestTable_LeavesNoCellBlank(t *testing.T) {
	t.Parallel()

	environments, err := merge([]byte(`[{"name": "orphan", "resourceCount": 0}]`),
		map[string]topologyFile{"planned": loaded(t, armWithAPool)})
	require.NoError(t, err)

	rendered := table(environments)
	lines := strings.Split(strings.TrimSpace(rendered), "\n")
	require.Len(t, lines, 3, "a header and one line per environment")

	assert.Contains(t, lines[0], "STACK")
	assert.Contains(t, lines[0], "RESOURCES")

	// The stack with no topology: the topology columns cannot be filled.
	assert.Contains(t, lines[1], "orphan")
	assert.Contains(t, lines[1], missing)
	assert.Contains(t, lines[1], "never", "a stack that exists and was never updated")

	// The topology with no stack: the backend columns cannot be filled.
	assert.Contains(t, lines[2], "planned")
	assert.Contains(t, lines[2], "fsn1")
	assert.Contains(t, lines[2], "arm")
}

// TestSummary_NamesTheEffectiveServerTypes is the reason this reads a loaded
// topology rather than the file.
//
// Every committed topology leaves the server types out and takes the default
// for its architecture, so printing the field verbatim would show a blank for
// the commonest case.
func TestSummary_NamesTheEffectiveServerTypes(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "3 x cx23", summary(loaded(t, sparse).Topology),
		"the x86 default, which the file does not state")
	assert.Equal(t, "1 x cax11, 2 x cax21", summary(loaded(t, armWithAPool).Topology),
		"the arm defaults, control plane first and then each pool")
}

func TestWhen_SaysNeverRatherThanNothing(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "never", when(""))
	assert.Equal(t, "2026-09-15 13:42Z", when("2026-09-15T13:42:52.000Z"))
	assert.Equal(t, "whenever", when("whenever"),
		"an unparseable stamp is Pulumi's own value, shown rather than dropped")
}

func TestRun_ListTakesOneArgument(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"no directory": {"list"},
		"too many":     {"list", "infra/cluster", "dev"},
	} {
		err := run(context.Background(), args)

		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "usage: stack list <cluster-dir>", name)
	}
}
