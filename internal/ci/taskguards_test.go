package ci

// The arguments a task takes, and what it refuses to do without them: the
// stack it will not assume, the layer it validates against a list, the usage
// it prints when one is missing, and the question it asks before changing
// infrastructure.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stackGuard is the precondition that turns a forgotten stack= into a usage
// message instead of a command aimed at nothing.
const stackGuard = `- sh: '[ -n "{{.stack}}" ]'`

// TestTasks_ThatNeedAStackSaySoWhenItIsMissing closes the gap left by removing
// the dev default.
//
// The default was the danger: `task platform:destroy layer=all` with a forgotten
// stack= meant dev, and the once that is wrong is the once it matters.
//
// Removing the default is not enough on its own, and the reason is worse than
// the one this comment used to give. It said an empty --stack would "fail
// somewhere less obvious". It does not fail at all — measured:
//
//	$ pulumi --non-interactive --stack "" preview
//	Previewing update (dev)
//
// Pulumi ignores the empty value and uses the stack selected in the
// workspace, which is invisible local state: whatever `pulumi stack select`
// left, or `tools/stack ensure` set during platform:init, or somebody's
// command from last week. So a forgotten word does not produce an error, it
// produces an operation aimed at whatever was selected last.
//
// A task whose dependency carries the guard is exempt, because Task runs
// dependencies before a task's own preconditions: the dependency's guard fires
// first, so a second one there would be unreachable.
func TestTasks_ThatNeedAStackSaySoWhenItIsMissing(t *testing.T) {
	t.Parallel()

	var checked int

	for _, path := range taskfiles(t) {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)

		tasks := tasksIn(string(raw))

		for name, body := range tasks {
			if !needsStack(body) {
				continue
			}

			checked++

			if strings.Contains(body, stackGuard) {
				continue
			}

			// No guard of its own is allowed only when a dependency carries
			// one: Task runs dependencies before a task's own preconditions,
			// so the dependency's guard fires first and a second one here
			// would be unreachable.
			//
			// Resolved rather than assumed. Exempting anything with a `deps:`
			// would let a task depend on an unguarded one and reach Pulumi
			// with an empty --stack.
			deps := dependenciesOf(body)
			require.NotEmpty(t, deps,
				"%s in %s reads the stack, has no guard, and depends on nothing that could carry one",
				name, filepath.Base(path))

			for _, dep := range deps {
				assert.Contains(t, tasks[dep], stackGuard,
					"%s in %s relies on %s for its stack guard, and %s has none",
					name, filepath.Base(path), dep, dep)
			}
		}
	}

	assert.Positive(t, checked, "no task reads a stack — this test is checking nothing")
}

// dependencies matches the inline list form this repository uses.
var dependencies = regexp.MustCompile(`deps: \[([^\]]+)\]`)

// dependenciesOf names the tasks a body declares as dependencies.
func dependenciesOf(body string) []string {
	match := dependencies.FindStringSubmatch(body)
	if match == nil {
		return nil
	}

	var out []string

	for _, dep := range strings.Split(match[1], ",") {
		out = append(out, strings.TrimSpace(dep))
	}

	return out
}

// moduleStack matches a taskfile module's own spelling of the stack: _CL_
// cluster, _PL_ platform, _PO_ policy, _BK_ backup.
//
// A pattern rather than the list this used to be. The list had already drifted
// once — _PO_STACK was missing, so every task in policy.task.yaml was
// invisible to both gates below — and a fourth module was added the day this
// comment was written. A var this matches and does not exist renders empty,
// which reaches Pulumi as `--stack ""` and fails loudly, so nothing hides
// behind the looser test.
var moduleStack = regexp.MustCompile(`\._[A-Z]{2}_STACK\b`)

// needsStack reports whether a task body reads the stack in any of its
// spellings.
func needsStack(body string) bool {
	return moduleStack.MatchString(body) || strings.Contains(body, "{{.stack}}")
}

// layerEnum matches the anchor that feeds every per-layer task's enum.
var layerEnum = regexp.MustCompile(`x-layers: &layers \[([^\]]+)\]`)

// layerList matches the whitespace-separated list the apply loop walks.
var layerList = regexp.MustCompile(`(?s)LAYERS: >-\n((?:    [^\n]+\n)+)`)

// TestLayerEnum_MatchesTheLayerList holds the two forms of the layer list
// equal.
//
// There are two because `requires.enum` is static schema: a template in it is
// read as a YAML map and the file stops parsing, so the enum cannot be built
// from LAYERS. One is a YAML list for the enum, the other the
// whitespace-separated string the apply loop walks — and a new layer added to
// only one of them fails the enum for a layer that exists, or lets a typo
// through for one that does not.
func TestLayerEnum_MatchesTheLayerList(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	platform, err := os.ReadFile(filepath.Join(root, "tasks", "platform.task.yaml"))
	require.NoError(t, err)

	rootfile, err := os.ReadFile(filepath.Join(root, "Taskfile.yaml"))
	require.NoError(t, err)

	enum := layerEnum.FindStringSubmatch(string(platform))
	require.NotNil(t, enum, "no x-layers anchor: the per-layer tasks have nothing to validate against")

	list := layerList.FindStringSubmatch(string(rootfile))
	require.NotNil(t, list, "no LAYERS list in the root taskfile")

	// `all` is the selector, not a layer, and it is in the enum so that
	// requires.vars keeps validating what was given — see the comment above
	// the anchor. Everything after it has to be the layer list exactly.
	values := splitEnum(enum[1])
	require.NotEmpty(t, values)
	assert.Equal(t, layerSelectorAll, values[0],
		"the enum's first value is not the whole-platform selector")

	assert.Equal(t, strings.Fields(list[1]), values[1:],
		"the layer enum and LAYERS disagree — one of them is missing a layer, or naming one that is gone")
}

// TestPerLayerTasks_ValidateAgainstTheAnchor stops a task from carrying its own
// copy of the list, which would drift without anything noticing.
func TestPerLayerTasks_ValidateAgainstTheAnchor(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "tasks", "platform.task.yaml"))
	require.NoError(t, err)

	var checked int

	for name, body := range tasksIn(string(raw)) {
		if !strings.Contains(body, "name: layer") {
			continue
		}

		checked++

		assert.Contains(t, body, "enum: *layers",
			"%s validates the layer against something other than the shared anchor", name)
	}

	// plan, apply, refresh, destroy, outputs. The count is the part that
	// catches drift: a new per-layer task that validated against its own copy
	// of the list would otherwise pass this test by not being looked at.
	assert.Equal(t, 5, checked, "five tasks take a layer; the count changed")
}

// splitEnum reads the items out of a YAML flow sequence.
func splitEnum(items string) []string {
	var out []string

	for _, item := range strings.Split(items, ",") {
		out = append(out, strings.TrimSpace(item))
	}

	return out
}

// layerSelectorAll is the enum value that means every layer.
//
// A word in the enum rather than an optional variable: requires.vars only
// validates what is given, so making `layer=` optional would drop the
// validation AND make a forgotten argument select the whole platform. The
// shortest command must not be the widest.
const layerSelectorAll = "all"

// changesTheCluster matches a task whose command writes — to infrastructure or
// to the state that describes it.
//
// `refresh` is in the list and looks like it should not be. It changes no
// infrastructure, which is exactly what makes it the dangerous one: where a
// resource is gone from the cloud it is REMOVED FROM STATE, and the next apply
// recreates it. A refresh against the wrong stack turns "somebody deleted a
// thing" into "Pulumi will now rebuild half a cluster", and it does that from
// a command that reads like a read.
var changesTheCluster = regexp.MustCompile(`pulumi[^\n]*\b(up|destroy|refresh)\b`)

// TestTasks_ThatChangeInfrastructureAskFirst is the guard for the confirmation
// a task carries.
//
// Destroy tasks always had one. Apply tasks did not, on the assumption that
// applying is the safe half of the pair — and it is not: `pulumi up` replaces
// a resource for any input that forces a replacement, and replacing the only
// control-plane server took this cluster down and needed a bootstrap to come
// back. Both verbs change a real cluster, so both ask.
//
// A task that runs one of them through another task is exempt: Task prompts
// per task it runs, so the inner prompt fires and a second one outside would
// make bringing up a cluster three questions instead of two.
func TestTasks_ThatChangeInfrastructureAskFirst(t *testing.T) {
	t.Parallel()

	var checked int

	for _, path := range taskfiles(t) {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)

		for name, body := range tasksIn(string(raw)) {
			if !changesTheCluster.MatchString(body) {
				continue
			}

			// `preview` is the read-only verb and shares no word with these.
			checked++

			assert.Contains(t, body, "prompt:",
				"%s: %s runs pulumi up, destroy or refresh with no confirmation",
				filepath.Base(path), name)
		}
	}

	// The count is the part that rots: a task that stops matching the pattern
	// — a different spelling, a wrapper — would silently leave this test
	// asserting nothing at all.
	assert.GreaterOrEqual(t, checked, 5,
		"fewer applying or destroying tasks found than exist; the pattern has drifted")
}

// TestDestroy_AsksAndSaysWhatSurvives holds the teardown's own confirmation.
//
// `destroy` is the one task that removes everything, and it escapes
// TestTasks_ThatChangeInfrastructureAskFirst by construction: that test looks
// for `pulumi destroy` in a task body, and this one delegates instead. A
// wrapper with no prompt would be the worst version of exactly what that test
// guards — one command, no question, nothing left.
//
// The prompt also has to name what SURVIVES. Its two children each ask about
// one half and neither mentions the secrets bundle, so without this the
// operator confirms an irreversible teardown without being told that the
// cluster CA is kept, or how to remove it on purpose.
func TestDestroy_AsksAndSaysWhatSurvives(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "Taskfile.yaml"))
	require.NoError(t, err)

	body, found := tasksIn(string(raw))["destroy"]
	require.True(t, found, "no `destroy` task in Taskfile.yaml")

	require.Contains(t, body, "prompt:", "`destroy` destroys everything with no confirmation")

	for _, mention := range []string{
		// The three the prompt must account for, because each is a thing
		// somebody looks for afterwards and does not find.
		"secrets bundle",
		"snapshot",
		"secrets:destroy",
	} {
		assert.Contains(t, body, mention,
			"`destroy` confirms an irreversible teardown without saying what happens to the %s", mention)
	}

	// Order is the other half, and getting it wrong is not cosmetic: servers
	// removed first leave every layer's state describing resources that are
	// gone.
	// Matched as task CALLS rather than as substrings, and the reason is worth
	// keeping even though the collision that caused it is gone: `cluster:destroy`
	// used to be a prefix of `cluster:destroy-secrets`, which the prompt names
	// above the commands, so a plain search found the wrong occurrence and
	// reported the order backwards. It did, the first time this test ran. The
	// task is `cluster:secrets:destroy` now and no longer collides — but a
	// prefix search over a file that names tasks in prose is the wrong tool
	// regardless.
	layers := strings.Index(body, "- task: platform:destroy")
	cluster := strings.Index(body, "- task: cluster:destroy\n")

	require.Positive(t, layers, "`destroy` does not destroy the layers")
	require.Positive(t, cluster, "`destroy` does not destroy the cluster")
	assert.Less(t, layers, cluster,
		"`destroy` destroys the cluster before its layers")
}

// TestTasks_DoNotDemandAStackTheyIgnore is the other direction of the guard
// above, and it is the one that was missing.
//
// `cluster:machine-config:check` asked for `stack=` and never read it: its
// command validates every topology in infra/cluster, so there was nothing
// per-stack about it. A new task then copied the shape —
// `cluster:talosctl:install` inherited the guard and, worse, a precondition on
// a topology file that is gitignored, which made it fail on exactly the fresh
// clone it existed for.
//
// Delegation counts as reading it. `plan` guards and names no stack, because
// it calls cluster:plan and platform:plan and Task hands CLI variables down —
// that guard is right, and failing it would be this test's own false positive.
// So a task is only reported when it guards, never names a stack, AND passes
// the work to nothing that could.
//
// The guard itself mentions {{.stack}}, so the body is read with the guard
// removed.
func TestTasks_DoNotDemandAStackTheyIgnore(t *testing.T) {
	t.Parallel()

	var checked int

	for _, path := range taskfiles(t) {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)

		for name, body := range tasksIn(string(raw)) {
			if !strings.Contains(body, stackGuard) {
				continue
			}

			checked++

			withoutGuard := strings.ReplaceAll(body, stackGuard, "")
			if needsStack(withoutGuard) || delegates(withoutGuard) {
				continue
			}

			assert.Fail(t,
				"a task guards on a stack it cannot use",
				"%s in %s guards on stack=, names no stack, and hands the work to no other "+
					"task. The value it asks for changes nothing: drop the guard, or read it",
				name, filepath.Base(path))
		}
	}

	assert.Positive(t, checked, "no task guards on a stack — this test is checking nothing")
}

// delegates reports whether a task hands work to another task, by dependency
// or by call. Either way the stack reaches that one through Task's own
// variable propagation, so a guard here is the early, readable failure.
func delegates(body string) bool {
	return strings.Contains(body, "deps:") || strings.Contains(body, "- task:")
}

// usageVar matches the `msg:` of a stack guard, which names the variable
// holding the message rather than spelling it out per task.
var usageVar = regexp.MustCompile(`msg: "\{\{\.(_[A-Z_]+)\}\}"`)

// multilineVar matches a taskfile's block-scalar variable — the form every
// usage message is written in.
var multilineVar = regexp.MustCompile(`(?m)^  (_[A-Z_]+): \|\n((?:(?:    .*)?\n)+)`)

// layerEnums matches every list of accepted layer= values: the anchor
// tasks/platform.task.yaml defines once, and the inline enum policy.task.yaml
// carries because its own list is shorter.
var layerEnums = regexp.MustCompile(`(?:x-layers: &layers|enum:) \[([^\]]+)\]`)

// suggestedLayer matches a layer= example inside a usage message.
var suggestedLayer = regexp.MustCompile(`layer=(\S+)`)

// requiresLayer is what makes layer= mandatory rather than optional.
const requiresLayer = "- name: layer"

// TestUsage_NamesEveryArgumentTheTaskRequires keeps a usage message from
// handing over a command that fails.
//
// Reported, and reproduced exactly:
//
//	$ task platform:plan layer=10-node-platform
//	platform:plan needs a stack, and there is no default.
//	    task platform:plan stack=dev
//	$ task platform:plan stack=dev
//	missing required variables: layer
//
// The guard was right both times and the message was wrong: it named the
// argument that was missing and dropped the one the operator had already got
// right, so following it cost a third attempt. A task that requires a layer
// has to say so in the message it prints when the stack is missing.
func TestUsage_NamesEveryArgumentTheTaskRequires(t *testing.T) {
	t.Parallel()

	messages := map[string]string{}

	for _, path := range taskfiles(t) {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)

		for _, match := range multilineVar.FindAllStringSubmatch(string(raw), -1) {
			messages[match[1]] = match[2]
		}
	}

	require.NotEmpty(t, messages, "no usage message was found, so this test proved nothing")

	var checked int

	for _, path := range taskfiles(t) {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)

		for name, body := range tasksIn(string(raw)) {
			if !strings.Contains(body, requiresLayer) {
				continue
			}

			named := usageVar.FindStringSubmatch(body)
			if named == nil {
				continue
			}

			checked++

			assert.Contains(t, messages[named[1]], "layer=",
				"%s in %s requires a layer, and %s — the message it prints when the stack "+
					"is missing — does not name one, so the command it suggests fails on the "+
					"argument the operator already passed",
				name, filepath.Base(path), named[1])
		}
	}

	assert.Positive(t, checked,
		"no task requires a layer and prints a usage message — this test is checking nothing")
}

// TestUsage_NamesALayerThatExists holds the example in a usage message to the
// enums that accept it.
//
// The message suggests one layer by name rather than layer=all, because
// platform:destroy prints it too and the shortest correct command there must
// not also be the widest — and policy:layer prints it while accepting no
// `all` at all. A named example is a copy of a list, and two lists have to
// accept it: a value only one of them takes would leave the other suggesting
// a command it rejects, which is the class of bug this pair was written for.
func TestUsage_NamesALayerThatExists(t *testing.T) {
	t.Parallel()

	var (
		messages []string
		enums    [][]string
	)

	for _, path := range taskfiles(t) {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)

		for _, match := range multilineVar.FindAllStringSubmatch(string(raw), -1) {
			messages = append(messages, match[2])
		}

		for _, match := range layerEnums.FindAllStringSubmatch(string(raw), -1) {
			var accepted []string

			for _, value := range strings.Split(match[1], ",") {
				accepted = append(accepted, strings.TrimSpace(value))
			}

			enums = append(enums, accepted)
		}
	}

	require.NotEmpty(t, enums, "no layer enum was found, so this test proved nothing")

	var checked int

	for _, message := range messages {
		for _, suggested := range suggestedLayer.FindAllStringSubmatch(message, -1) {
			checked++

			for _, accepted := range enums {
				assert.Contains(t, accepted, suggested[1],
					"a usage message suggests layer=%s, and one of the tasks printing it "+
						"accepts only %v", suggested[1], accepted)
			}
		}
	}

	assert.Positive(t, checked, "no layer example was examined, so this test proved nothing")
}
