package repo

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// taskName matches a task declaration at the top of a taskfile's tasks block.
var taskName = regexp.MustCompile(`^  ([a-zA-Z_][\w:-]*):\s*$`)

// stackGuard is the precondition that turns a forgotten stack= into a usage
// message instead of a command aimed at nothing.
const stackGuard = `- sh: '[ -n "{{.stack}}" ]'`

// taskfiles is every taskfile in the repository: the root one and each
// include.
//
// Derived from the directory rather than listed, because both tests below
// walked a hardcoded list of three — so a new taskfile would not have been
// checked and nothing would have said so. That is the failure mode these
// tests exist to prevent, one level up.
func taskfiles(t *testing.T) []string {
	t.Helper()

	root := filepath.Join("..", "..")

	includes, err := filepath.Glob(filepath.Join(root, "tasks", "*.task.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, includes, "no taskfiles under tasks/")

	return append([]string{filepath.Join(root, "Taskfile.yaml")}, includes...)
}

// TestTasks_ThatNeedAStackSaySoWhenItIsMissing closes the gap left by removing
// the dev default.
//
// The default was the danger: `task platform:destroy-all` with a forgotten
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

// needsStack reports whether a task body reads the stack in any of its
// spellings.
func needsStack(body string) bool {
	for _, spelling := range []string{"._CL_STACK", "._PL_STACK", "{{.stack}}"} {
		if strings.Contains(body, spelling) {
			return true
		}
	}

	return false
}

// tasksIn splits a taskfile's tasks block into one body per task.
func tasksIn(text string) map[string]string {
	_, after, found := strings.Cut(text, "\ntasks:\n")
	if !found {
		return nil
	}

	tasks := map[string]string{}

	var (
		current string
		body    strings.Builder
	)

	flush := func() {
		if current != "" {
			tasks[current] = body.String()
		}

		body.Reset()
	}

	for _, line := range strings.Split(after, "\n") {
		if match := taskName.FindStringSubmatch(line); match != nil {
			flush()

			current = match[1]

			continue
		}

		body.WriteString(line + "\n")
	}

	flush()

	return tasks
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

	assert.Equal(t, strings.Fields(list[1]), splitEnum(enum[1]),
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

	assert.Equal(t, 4, checked, "four tasks take a layer; the count changed")
}

// splitEnum reads the items out of a YAML flow sequence.
func splitEnum(items string) []string {
	var out []string

	for _, item := range strings.Split(items, ",") {
		out = append(out, strings.TrimSpace(item))
	}

	return out
}

// changesTheCluster matches a task whose command applies or destroys real
// infrastructure, by the two verbs Pulumi has for it.
var changesTheCluster = regexp.MustCompile(`pulumi[^\n]*\b(up|destroy)\b`)

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
				"%s: %s runs `pulumi up` or `pulumi destroy` with no confirmation",
				filepath.Base(path), name)
		}
	}

	// The count is the part that rots: a task that stops matching the pattern
	// — a different spelling, a wrapper — would silently leave this test
	// asserting nothing at all.
	assert.GreaterOrEqual(t, checked, 5,
		"fewer applying or destroying tasks found than exist; the pattern has drifted")
}

// TestSnapshotSidecar_IsSpelledOnce holds the writer and the reader of a
// snapshot's .info file to one spelling.
//
// cluster:etcd-snapshot writes it and cluster:etcd-restore reads it, and a
// mismatch between them is not an error anybody sees: the restore simply
// stops printing what the snapshot was recorded as containing, which is the
// one thing that would say the file changed after it was written.
func TestSnapshotSidecar_IsSpelledOnce(t *testing.T) {
	t.Parallel()

	const (
		sidecarVar    = "_CL_SNAPSHOT_INFO"
		sidecarSuffix = ".info"
	)

	raw, err := os.ReadFile(filepath.Join("..", "..", "tasks", "cluster.task.yaml"))
	require.NoError(t, err)

	tasks := tasksIn(string(raw))

	for _, name := range []string{"etcd-snapshot", "etcd-restore"} {
		body, found := tasks[name]
		require.True(t, found, "no %s task to check", name)

		assert.Contains(t, body, sidecarVar,
			"%s does not use {{.%s}}, so it carries its own spelling of the sidecar's name",
			name, sidecarVar)

		// The var alone is not enough: a body can mention it in a message and
		// still build the path from a literal, which is exactly the drift
		// this is here to stop. The suffix itself lives in the vars block,
		// which is not part of any task body.
		assert.NotContains(t, body, sidecarSuffix,
			"%s spells the sidecar suffix %q itself instead of using {{.%s}}",
			name, sidecarSuffix, sidecarVar)
	}
}

// The guard that used to sit here — every hcloud verb that takes a node away
// must carry a prompt — went with the tasks. They are the shared library's
// `hcloud` module now, and a gate belongs where the thing it guards lives:
// this repository declares no hcloud task to check.

// documentedTask matches a task named in prose or a table: `task cluster:plan`.
var documentedTask = regexp.MustCompile("`task ([a-z][a-z0-9:_-]*)")

// declaredTask matches a task declaration inside a taskfile.
var declaredTask = regexp.MustCompile(`(?m)^  ([a-z][a-z0-9:_-]*):\s*$`)

// TestDocs_NameOnlyTasksThatExist guards the one documentation error that
// wastes an operator's time rather than merely misleading them: a command
// they copy, paste and watch fail.
//
// It is not hypothetical. Renaming a task is a two-file edit that looks like
// one, and this repository has renamed several — cluster:power-status became
// hcloud:servers in the same change that moved it. Nothing else compares the
// two sides.
//
// Included tasks come from the shared library and are not declared here, so
// their names are checked against `task --list` being available rather than
// against a file. Anything namespaced by an include that this repository does
// not declare is skipped: the alternative is running Task from a test.
func TestDocs_NameOnlyTasksThatExist(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	declared := map[string]bool{}

	for _, path := range taskfiles(t) {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)

		namespace := ""
		if base := filepath.Base(path); base != "Taskfile.yaml" {
			namespace = strings.TrimSuffix(base, ".task.yaml") + ":"
		}

		for _, found := range declaredTask.FindAllStringSubmatch(string(raw), -1) {
			declared[namespace+found[1]] = true
		}
	}

	require.NotEmpty(t, declared, "no tasks found to compare against")

	docs, err := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	require.NoError(t, err)

	var checked int

	for _, path := range append(docs, filepath.Join(root, "README.md")) {
		// BACKLOG.md is planning, and planning names the task it wants
		// before that task exists — which is the point of writing it down.
		// It is gitignored for the same reason it is skipped here: it is not
		// documentation an operator copies from. On a clean clone the glob
		// never finds it.
		if filepath.Base(path) == "BACKLOG.md" {
			continue
		}

		raw, err := os.ReadFile(path)
		require.NoError(t, err)

		for _, found := range documentedTask.FindAllStringSubmatch(string(raw), -1) {
			name := found[1]

			// A namespace this repository does not own belongs to the shared
			// library, whose tasks are not in any file here.
			if namespace, _, found := strings.Cut(name, ":"); found && !ownsNamespace(declared, namespace) {
				continue
			}

			checked++

			assert.True(t, declared[name],
				"%s names `task %s`, which no taskfile declares", filepath.Base(path), name)
		}
	}

	assert.Positive(t, checked, "no documented task names found; the pattern has drifted")
}

// ownsNamespace reports whether this repository declares any task in it.
func ownsNamespace(declared map[string]bool, namespace string) bool {
	for name := range declared {
		if strings.HasPrefix(name, namespace+":") {
			return true
		}
	}

	return false
}

// glyphVar matches a marker definition in the root taskfile, and goGlyph the
// matching constant in the logger.
var (
	glyphVar = regexp.MustCompile(`(?m)^  _([A-Z]+): '\{\{if \.NO_COLOR\}\}(.)\{\{else\}\}\{\{"\\x1b\[(\d+)m(.)\\x1b\[0m"\}\}`)
	goGlyph  = regexp.MustCompile(`(?m)^\t(Glyph\w+)\s+= "(.)"`)
	goColour = regexp.MustCompile(`(?m)^\t(colour\w+)\s+= "\\x1b\[(\d+)m"`)
)

// TestTaskGlyphs_MatchThePulumiLogger holds the two halves of one vocabulary
// equal.
//
// pkg/pulumilog says its glyphs match the taskfiles "exactly" and that
// changing one without the other is how two tools stop looking like one — and
// nothing checked it. A task and the program it runs print into one terminal.
func TestTaskGlyphs_MatchThePulumiLogger(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	taskfile, err := os.ReadFile(filepath.Join(root, "Taskfile.yaml"))
	require.NoError(t, err)

	logger, err := os.ReadFile(filepath.Join(root, "pkg", "pulumilog", "pulumilog.go"))
	require.NoError(t, err)

	glyphs := map[string]string{}
	for _, found := range goGlyph.FindAllStringSubmatch(string(logger), -1) {
		glyphs[found[1]] = found[2]
	}

	require.NotEmpty(t, glyphs, "no glyph constants in pulumilog")

	colours := map[string]bool{}
	for _, found := range goColour.FindAllStringSubmatch(string(logger), -1) {
		colours[found[2]] = true
	}

	// The taskfile's marker name → the logger's constant. ERR has no pair:
	// a layer reports failure by returning an error, which Pulumi formats
	// itself, so pulumilog deliberately has no error glyph.
	for marker, constant := range map[string]string{
		"RUN":  "GlyphRunning",
		"OK":   "GlyphOK",
		"SKIP": "GlyphSkipped",
		"WARN": "GlyphWarning",
	} {
		glyph, colour := glyphOf(t, string(taskfile), marker)

		assert.Equal(t, glyphs[constant], glyph,
			"_%s in the taskfile and %s in pulumilog are different glyphs", marker, constant)
		assert.True(t, colours[colour],
			"_%s is coloured \\x1b[%sm in the taskfile, which pulumilog does not use", marker, colour)
	}
}

// glyphOf returns one marker's glyph and its ANSI colour code, and fails if
// the plain and coloured halves of the definition disagree.
func glyphOf(t *testing.T, taskfile, marker string) (glyph, colour string) {
	t.Helper()

	for _, found := range glyphVar.FindAllStringSubmatch(taskfile, -1) {
		if found[1] != marker {
			continue
		}

		require.Equal(t, found[2], found[4],
			"_%s prints one glyph without colour and another with it", marker)

		return found[2], found[3]
	}

	t.Fatalf("no _%s marker in the root taskfile", marker)

	return "", ""
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
		"destroy-secrets",
	} {
		assert.Contains(t, body, mention,
			"`destroy` confirms an irreversible teardown without saying what happens to the %s", mention)
	}

	// Order is the other half, and getting it wrong is not cosmetic: servers
	// removed first leave every layer's state describing resources that are
	// gone.
	// Matched as task CALLS rather than as substrings. `cluster:destroy` is a
	// prefix of `cluster:destroy-secrets`, which the prompt names above the
	// commands — so a plain search finds the wrong occurrence and reports the
	// order backwards. It did, the first time this test ran.
	layers := strings.Index(body, "- task: platform:destroy-all")
	cluster := strings.Index(body, "- task: cluster:destroy\n")

	require.Positive(t, layers, "`destroy` does not destroy the layers")
	require.Positive(t, cluster, "`destroy` does not destroy the cluster")
	assert.Less(t, layers, cluster,
		"`destroy` destroys the cluster before its layers")
}
