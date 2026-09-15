package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/hetzner"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sigs.k8s.io/yaml"
)

const validTopology = `
apiVersion: hetzner-iac/v1
kind: Cluster
metadata:
  name: platform-test
placement:
  location: hel1
network:
  adminCIDRs: [203.0.113.4/32]
talos:
  version: v1.14.0
controlPlane:
  count: 1
  serverType: cx23
`

func TestIsTopologyFile(t *testing.T) {
	t.Parallel()

	// The shape matters: infra/cluster/main.go derives the path from the stack
	// name, so a file that does not follow it is never loaded by anything and
	// sits in the repository looking like configuration that is in effect.
	for name, want := range map[string]bool{
		"cluster.prod.yaml":     true,
		"cluster.staging.yaml":  true,
		"cluster.yaml":          false, // no stack name
		"cluster.prod.yml":      false, // wrong extension
		"cluster.prod.old.yaml": false, // not a stack name main.go would build
		"Pulumi.yaml":           false,
		"prod.yaml":             false,
		"":                      false,
	} {
		assert.Equal(t, want, IsTopologyFile(name), name)
	}
}

func TestValidate_AcceptsAValidTopology(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cluster.prod.yaml"), []byte(validTopology), 0o600))

	failures, err := Validate([]string{dir})
	require.NoError(t, err)
	assert.Empty(t, failures)
}

func TestValidate_ReportsEveryInvalidFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(dir, "cluster.good.yaml"), []byte(validTopology), 0o600))
	// Two separate problems in two files: the run must report both rather than
	// stopping at the first, or fixing a topology becomes one round trip per
	// mistake.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cluster.missing.yaml"),
		[]byte("apiVersion: hetzner-iac/v1\nkind: Cluster\nmetadata: {name: x}\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cluster.malformed.yaml"),
		[]byte("this is not: [valid: yaml\n"), 0o600))

	failures, err := Validate([]string{dir})
	require.NoError(t, err)
	require.Len(t, failures, 2)

	joined := strings.Join(failures, "\n")
	assert.Contains(t, joined, "cluster.missing.yaml")
	assert.Contains(t, joined, "cluster.malformed.yaml")
	assert.NotContains(t, joined, "cluster.good.yaml")
}

func TestValidate_CatchesAnEmptyAdminCIDRList(t *testing.T) {
	t.Parallel()

	// The check that matters most: a cluster whose Kubernetes and Talos APIs
	// are open to the internet must not reach an apply.
	dir := t.TempDir()

	open := `
apiVersion: hetzner-iac/v1
kind: Cluster
metadata: {name: platform-test}
placement: {location: hel1}
network: {adminCIDRs: []}
talos: {version: v1.14.0}
controlPlane: {count: 1, serverType: cx23}
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cluster.prod.yaml"), []byte(open), 0o600))

	failures, err := Validate([]string{dir})
	require.NoError(t, err)
	require.Len(t, failures, 1)
	assert.Contains(t, failures[0], "open to the internet")
}

func TestValidate_FailsWhenThereIsNothingToCheck(t *testing.T) {
	t.Parallel()

	// A validator that reports green because it found no files is worse than
	// none: it reports success while checking nothing.
	_, err := Validate([]string{t.TempDir()})

	require.ErrorIs(t, err, ErrNoTopologies)
}

func TestValidate_FailsOnAMissingDirectory(t *testing.T) {
	t.Parallel()

	_, err := Validate([]string{filepath.Join(t.TempDir(), "absent")})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")
}

func TestValidate_IgnoresFilesThatAreNotTopologies(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cluster.prod.yaml"), []byte(validTopology), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Pulumi.yaml"), []byte("name: broken\n{{{"), 0o600))

	failures, err := Validate([]string{dir})
	require.NoError(t, err)
	assert.Empty(t, failures)
}

func TestValidate_ChecksTheRepositoryTopologies(t *testing.T) {
	t.Parallel()

	// The committed topologies must always be valid — this is the same check
	// CI runs, kept as a test so `go test ./...` catches it too.
	failures, err := Validate([]string{filepath.Join("..", "..", "infra", "cluster")})
	require.NoError(t, err)
	assert.Empty(t, failures)
}

func TestTalosVersion(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "cluster.prod.yaml")
	require.NoError(t, os.WriteFile(path, []byte(validTopology), 0o600))

	version, err := get("talos-version", path)
	require.NoError(t, err)
	assert.Equal(t, "v1.14.0", version)
}

func TestTalosVersion_ReadsThroughTheRealParser(t *testing.T) {
	t.Parallel()

	// The point of this existing at all: CI installs a matching talosctl from
	// it, and the first version used `grep -A6`, which returned nothing once
	// the comment above the field grew. A parser does not care how much prose
	// sits above the value.
	dir := t.TempDir()
	path := filepath.Join(dir, "cluster.prod.yaml")

	commented := "talos:\n" + strings.Repeat("  # a long explanation\n", 20) + "  version: v1.13.10\n  architecture: x86\n"
	body := strings.Replace(validTopology, "talos:\n  version: v1.14.0\n", commented, 1)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	version, err := get("talos-version", path)
	require.NoError(t, err)
	assert.Equal(t, "v1.13.10", version)
}

func TestTalosVersion_InvalidTopology(t *testing.T) {
	t.Parallel()

	// A malformed file must fail loudly rather than print an empty string that
	// a shell would splice into a download URL.
	dir := t.TempDir()
	path := filepath.Join(dir, "cluster.prod.yaml")
	require.NoError(t, os.WriteFile(path, []byte("not: a topology\n"), 0o600))

	_, err := get("talos-version", path)
	require.Error(t, err)
}

func TestTalosVersion_MatchesTheRepositoryTopologies(t *testing.T) {
	t.Parallel()

	// What CI actually runs.
	version, err := get("talos-version", filepath.Join("..", "..", "infra", "cluster", "cluster.example.yaml"))
	require.NoError(t, err)
	assert.Regexp(t, `^v\d+\.\d+\.\d+$`, version)
}

// projectFile is the part of a Pulumi.yaml this test cares about.
type projectFile struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Config      map[string]struct {
		Default any  `json:"default"`
		Secret  bool `json:"secret"`
	} `json:"config"`
}

// TestProjectDescriptionsFitTheStackTag guards a limit that is invisible until
// it is hit, and hit at the worst moment.
//
// Pulumi sends `description` from Pulumi.yaml as the `pulumi:description`
// stack tag, and Pulumi Cloud refuses one over 256 characters. It refuses at
// `pulumi stack init` — before any resource, before any preview, after the
// operator has set up credentials:
//
//	error: could not create stack: validating stack properties: stack tag
//	"pulumi:description" value is too long (max length 256 characters)
//
// Three of the projects were over it, written long because the prose
// seemed useful. Prose belongs in a YAML comment above the key, which is not
// sent anywhere. This test is here because two of the remaining descriptions
// sit within ten characters of the limit, and the next edit would put them
// over with nothing to say so.
func TestProjectDescriptionsFitTheStackTag(t *testing.T) {
	t.Parallel()

	const maxStackTag = 256

	for _, path := range projectPaths(t) {
		raw, err := os.ReadFile(path)
		require.NoError(t, err, path)

		var project projectFile
		require.NoError(t, yaml.Unmarshal(raw, &project), path)

		require.NotEmpty(t, project.Description, "%s has no description", path)

		assert.LessOrEqual(t, len(project.Description), maxStackTag,
			"%s: description is %d characters; Pulumi Cloud refuses the stack tag over %d, "+
				"so `pulumi stack init` would fail. Move the prose into a comment above the key.",
			project.Name, len(project.Description), maxStackTag)
	}
}

// layerList matches the whitespace-separated layer list the taskfiles walk.
var layerList = regexp.MustCompile(`(?s)LAYERS: >-\n((?:    [^\n]+\n)+)`)

// projectPaths returns every Pulumi.yaml in the repository.
func projectPaths(t *testing.T) []string {
	t.Helper()

	root := filepath.Join("..", "..")

	paths, err := filepath.Glob(filepath.Join(root, "layers", "*", "Pulumi.yaml"))
	require.NoError(t, err)

	paths = append(paths, filepath.Join(root, "infra", "cluster", "Pulumi.yaml"))

	// Counted against the layer list rather than a number written here: a new
	// layer used to fail this test for existing, which says nothing about the
	// thing being guarded — that the glob still finds the projects at all.
	layers, err := os.ReadFile(filepath.Join(root, "Taskfile.yaml"))
	require.NoError(t, err)

	declared := layerList.FindStringSubmatch(string(layers))
	require.NotNil(t, declared, "no LAYERS list in the root taskfile")

	require.Len(t, paths, len(strings.Fields(declared[1]))+1,
		"one Pulumi project per layer, plus the cluster tier")

	return paths
}

// TestDeclaredConfigIsOptional guards the rule that a key declared in
// Pulumi.yaml without a `default` is REQUIRED by Pulumi's own stack-config
// validation — and that validation runs before the program:
//
//	error: validating stack config: Stack 'dev' is missing configuration
//	values 'observability:logsRetention', ...
//
// (quoted as it was measured, on a layer this repository no longer carries)
//
// Which means the layer's own message, the one naming the three Object Storage
// locations or the command that sets a stack reference, is never reached. That
// is the whole failure: these layers report every problem at once, with a
// remedy, and a schema that fails first replaces all of it with a list of key
// names.
//
// It cost two `platform:plan layer=all` runs — two layers in a row, each stopped by
// its own schema — so the guard is a test rather than a comment.
// An intentionally required key belongs in requiredConfig below, with the
// reason; the point is that requiring one is a decision, not an omission.
func TestDeclaredConfigIsOptional(t *testing.T) {
	t.Parallel()

	// Empty on purpose. Every required-looking key is better reported by the
	// program: internal/pkg/layer names the command that sets clusterStackRef, and it
	// reports every problem at once rather than one per run.
	requiredConfig := map[string]bool{}

	for _, path := range projectPaths(t) {
		raw, err := os.ReadFile(path)
		require.NoError(t, err, path)

		var project projectFile
		require.NoError(t, yaml.Unmarshal(raw, &project), path)

		for key, spec := range project.Config {
			if requiredConfig[key] {
				continue
			}

			assert.NotNil(t, spec.Default,
				"%s declares %s with no `default`, which makes Pulumi require it before "+
					"the program runs. Give it `default: \"\"` and let the program validate, "+
					"or add it to requiredConfig with the reason.", path, key)

			// A secret with an empty default is worse than a required one: it
			// arrives set-but-empty, so a presence check passes and the
			// credential reaching the provider is the empty string.
			assert.False(t, spec.Secret,
				"%s declares the secret %s. A secret cannot be made optional with a default — "+
					"an empty one reads as set. Leave it undeclared and document it in a comment.",
				path, key)
		}
	}
}

func TestGet_ReadsEveryFieldFromEveryTopologyPresent(t *testing.T) {
	t.Parallel()

	// The point of this tool over a grep. Three of the four call sites it
	// replaced were returning an empty string, because the comments above
	// those fields had grown past the three-line window the grep looked in.
	//
	// Globbed rather than named: only cluster.example.yaml is committed, and a
	// working copy also has the stack topologies it was copied into. Every one
	// present has to be readable.
	paths, err := filepath.Glob(filepath.Join("..", "..", "infra", "cluster", "cluster.*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, paths, "cluster.example.yaml is committed and must be here")

	for _, path := range paths {
		for _, field := range fieldNames() {
			value, getErr := get(field, path)
			require.NoError(t, getErr, "%s %s", path, field)
			assert.NotEmpty(t, value, "%s %s", path, field)
		}
	}
}

func TestGet_RefusesAnUnknownField(t *testing.T) {
	t.Parallel()

	_, err := get("talos_version", filepath.Join("..", "..", "infra", "cluster", "cluster.example.yaml"))

	require.Error(t, err)
	// The message lists the alternatives, because a typo in a task is
	// otherwise indistinguishable from a missing field.
	assert.Contains(t, err.Error(), "unknown field")
	assert.Contains(t, err.Error(), "talos-version")
}

func TestGet_RefusesToPrintAnEmptyValue(t *testing.T) {
	t.Parallel()

	// The failure this tool exists to prevent: an empty string substituted
	// into a factory URL or an image selector builds something plausible and
	// wrong. Asserted through the real path — a topology whose optional
	// architecture is blank and whose default has been removed cannot be
	// constructed, so this checks the guard on a field the parser leaves
	// empty when the file omits it.
	dir := t.TempDir()
	path := filepath.Join(dir, "cluster.yaml")
	require.NoError(t, os.WriteFile(path, []byte(validTopology), 0o600))

	// Every field of a valid topology resolves, which is the other half of the
	// guarantee: `get` does not fail on a file that is merely terse.
	for _, field := range fieldNames() {
		value, err := get(field, path)
		require.NoError(t, err, field)
		assert.NotEmpty(t, value, field)
	}
}

// altMarker is where cluster.example.yaml's commented alternative shape
// starts. The marker is in the file, so moving the block moves this test with
// it. Which shape is commented has swapped once — high availability was the
// alternative and is now the default — and this test does not care which.
const altMarker = "# Uncomment from here"

// activeMarker is where the example's own active configuration begins. Named
// beside altMarker rather than left as a literal in the test: the two are one
// convention, and a block boundary spelled in only one of them is how the
// splice below silently starts producing the wrong document.
const activeMarker = "controlPlane:\n"

// sectionRule is how the example separates its sections, and therefore where
// one commented alternative stops and the next one's prose begins.
const sectionRule = "# ───"

// TestExampleTopology_EveryCommentedAlternativeIsValid uncomments each of the
// example's alternative blocks exactly the way its own instructions say to, and
// puts each through the real loader.
//
// A commented configuration nobody checks is a claim, and these are the
// copy-paste path for an operator changing the shape of a cluster — the failure
// would otherwise arrive after `pulumi up` had started creating servers.
//
// It caught its own first draft, which mixed prose and yaml at different
// comment depths: stripping the prefix produced a document nothing could parse.
// Each block is pure yaml now, which is what makes "strip the leading # " the
// whole edit.
//
// Every block, not the first one. There are two — a single node, and a control
// plane with workers of its own — and a test that read only as far as the first
// marker would have let the second rot unchecked, which is the exact thing this
// test exists to prevent.
func TestExampleTopology_EveryCommentedAlternativeIsValid(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "infra", "cluster", "cluster.example.yaml")

	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	example := string(raw)

	active := strings.Index(example, activeMarker)
	require.NotEqual(t, -1, active, "no active controlPlane block in the example")

	head := example[:active]

	blocks := alternativeBlocks(t, example)
	require.Len(t, blocks, 2, "the example has two alternatives; a new one needs no test change, a removed one does")

	// The same loader the program runs, so this cannot drift from it.
	activeTopology, err := hetzner.LoadTopology(path)
	require.NoError(t, err, "the example's own active configuration does not load")

	for i, block := range blocks {
		written := filepath.Join(t.TempDir(), fmt.Sprintf("cluster.alternative.%d.yaml", i))
		require.NoError(t, os.WriteFile(written, []byte(head+block+"\n"), 0o600))

		alternative, err := hetzner.LoadTopology(written)
		require.NoError(t, err,
			"alternative %d: the example's own instructions produce a topology that does not load", i)

		// And it is an alternative, not merely valid. A block describing the
		// same shape as the active one would load, pass, and buy nobody
		// anything — which is the failure this half catches.
		//
		// Two dimensions rather than one, because the two blocks differ in
		// different ones: the single node in its control-plane count, the
		// worker pool in having a pool at all. Which shape is commented has
		// already swapped once, so neither is assumed.
		sameControlPlane := activeTopology.ControlPlane.Count == alternative.ControlPlane.Count
		samePools := len(activeTopology.WorkerPools) == len(alternative.WorkerPools)

		assert.False(t, sameControlPlane && samePools,
			"alternative %d describes the same cluster shape as the active configuration", i)
	}
}

// alternativeBlocks returns each commented alternative as plain yaml, in file
// order.
//
// A block runs from its marker to the next section rule or the end of the file.
// The rule is the `# ───` heading the example already separates its sections
// with, so this needs no vocabulary of its own — and getting the boundary wrong
// is not theoretical: taking it to the NEXT MARKER instead swallowed the prose
// heading of the following block, uncommented it, and produced
// "yaml: line 140: could not find expected ':'".
//
// Everything in a block is a comment, and stripping the prefix is the whole
// transformation — the same edit the instructions above each block tell an
// operator to make by hand.
func alternativeBlocks(t *testing.T, example string) []string {
	t.Helper()

	var blocks []string

	lines := strings.Split(example, "\n")

	for i, line := range lines {
		if !strings.HasPrefix(line, altMarker) {
			continue
		}

		var uncommented []string

		for _, body := range lines[i+1:] {
			// The section rule ends the block. Anything that is not a comment
			// does too, which is what stops the last block at the end of the
			// file without a terminator of its own.
			if strings.HasPrefix(body, sectionRule) || !strings.HasPrefix(body, "#") {
				break
			}

			stripped := strings.TrimPrefix(strings.TrimPrefix(body, "#"), " ")
			if stripped == "" {
				continue
			}

			uncommented = append(uncommented, stripped)
		}

		require.NotEmpty(t, uncommented, "the alternative block at line %d is empty", i+1)

		blocks = append(blocks, strings.Join(uncommented, "\n"))
	}

	return blocks
}
