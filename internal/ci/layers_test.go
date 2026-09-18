package ci

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/workloads"
)

// TestEveryLayer_ChecksItsComponents closes the gap that made the other checks
// optional.
//
// internal/pkg/layer/layertest holds the invariants a component set must satisfy — the
// chart is pinned, the order has no cycle, every chart has workloads declared.
// Nothing made a layer call it. A new layer added without that one line got
// none of them and nothing said so, which is the same shape as every other
// drift this repository has fixed today: two halves, and no test comparing
// them.
// handBuilt are the layers that create their resources without a
// layer.Components table, each for a reason its own package comment gives.
var handBuilt = []string{}

func TestEveryLayer_ChecksItsComponents(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	entries, err := os.ReadDir(filepath.Join(root, "layers"))
	require.NoError(t, err)

	var checked int

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		layer := entry.Name()
		dir := filepath.Join(root, "layers", layer)

		if !contains(t, dir, "layer.Components{") {
			// Building the resources by hand is allowed, but only on
			// purpose. Unnamed, the skip makes a layer that forgot its
			// table indistinguishable from one that has a reason, and
			// the check reports green for both.
			assert.Contains(t, handBuilt, layer,
				"layers/%s declares no component set and is not named as an exception: "+
					"either give it a layer.Components table, or say in its package "+
					"comment why it builds its resources by hand and name it here", layer)

			continue
		}

		// The call, not one exact spelling of it. layers/40-ingress builds its
		// set with a function — one component needs the Hetzner provider,
		// which exists only once the cluster tier's token is read — so the
		// argument is components(nil) there, and grepping for `Components)`
		// would have called that layer unchecked while it is checked.
		assert.True(t, contains(t, dir, "layertest.Check(t, "),
			"layers/%s declares a component set but never calls layertest.Check, "+
				"so none of the invariants are asserted for it", layer)

		checked++
	}

	assert.Positive(t, checked, "no layer declares a component set — this test is checking nothing")
}

// contains reports whether any Go file in dir holds the given text.
func contains(t *testing.T, dir, text string) bool {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err, dir)

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}

		raw, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
		require.NoError(t, readErr)

		if strings.Contains(string(raw), text) {
			return true
		}
	}

	return false
}

// layerReference matches a layer named by its path, which is the only spelling
// this test can check — and therefore the spelling every comment must use.
//
// A bare "10-cni" could be a directory that once existed or a sentence about
// one, and no test can tell those apart. With the path prefix required, a
// reference either resolves or it is a defect, and prose about a layer that
// was removed is exempt by construction.
var layerReference = regexp.MustCompile(`layers/[0-9]{2}-[a-z][a-z0-9-]*`)

// TestEveryLayerReference_PointsAtADirectoryThatExists catches the comment
// that outlives the thing it describes.
//
// Merging two layers left eight comments pointing readers at two directories
// that no longer exist — 10-cni and 20-cloud-integration — including the
// package that explains why the CNI is not installed with the cluster, which
// is exactly where a reader goes to understand that. One was found by eye,
// months later. Nothing would have found the rest.
//
// The names above are deliberately written without the path prefix: this test
// reads its own source like any other file.
func TestEveryLayerReference_PointsAtADirectoryThatExists(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	entries, err := os.ReadDir(filepath.Join(root, "layers"))
	require.NoError(t, err)

	existing := map[string]bool{}

	for _, entry := range entries {
		if entry.IsDir() {
			existing["layers/"+entry.Name()] = true
		}
	}

	require.NotEmpty(t, existing, "no layers found — this test is checking nothing")

	var references int

	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if entry.IsDir() {
			// .git holds every version of every file, including the ones that
			// did name the layers that are gone.
			if entry.Name() == ".git" {
				return fs.SkipDir
			}

			return nil
		}

		if !slices.Contains([]string{".go", ".yaml", ".yml", ".json", ".md"}, filepath.Ext(entry.Name())) {
			return nil
		}

		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}

		for _, reference := range layerReference.FindAllString(string(raw), -1) {
			references++

			assert.True(t, existing[reference],
				"%s names %s, which is not a layer in this tree", path, reference)
		}

		return nil
	})
	require.NoError(t, err)

	assert.Positive(t, references, "no file names a layer by path — the pattern must be wrong")
}

// TestBackupLayer_ExportsThePasswordAsASecret is the one property of
// infra/backup worth pinning in source, because getting it wrong is
// silent and the consequence is a credential in plaintext.
//
// A stack output is what somebody copies. `pulumi stack output backupPassword`
// on an unwrapped export prints the password; wrapped, it prints `[secret]`
// and needs `--show-secrets`, which is a deliberate act. The rest of that
// layer is wiring whose logic lives in internal/pkg/hetzner, and is tested there.
func TestBackupLayer_ExportsThePasswordAsASecret(t *testing.T) {
	t.Parallel()

	// Every Go file in that layer, for the reason the outputs gate reads them
	// all: a file added beside main.go could carry the export away and leave
	// this reading the wrong one.
	sources, err := filepath.Glob(filepath.Join("..", "..", "infra", "backup", "*.go"))
	require.NoError(t, err)
	require.NotEmpty(t, sources)

	var joined strings.Builder

	for _, path := range sources {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}

		raw, readErr := os.ReadFile(path)
		require.NoError(t, readErr, path)

		joined.Write(raw)
	}

	body := joined.String()

	require.Contains(t, body, "OutputPassword",
		"the backup tier no longer exports a password; this test is checking nothing")

	assert.Regexp(t, `Export\(OutputPassword, pulumi\.ToSecret\(`, body,
		"the backup tier exports the password without pulumi.ToSecret, so "+
			"`pulumi stack output` prints the credential")
}

// TestNoLayerIsNamedAll keeps the selector unambiguous.
//
// `all` is a value of the same enum the layer names are in, so a layer
// directory called `all` would make `layer=all` mean two things: that layer,
// or every layer. Task would accept it and the loop would do whichever the
// code happened to check first.
//
// Cheap to guard and impossible to notice otherwise: the enum would still
// validate, the task would still run, and the wrong set of stacks would be
// applied.
func TestNoLayerIsNamedAll(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(filepath.Join("..", "..", "layers"))
	require.NoError(t, err)

	var names []string

	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}

	require.NotEmpty(t, names, "no layers found; this test is checking nothing")
	assert.NotContains(t, names, "all",
		"a layer directory named `all` collides with the whole-platform selector")
}

// TestWorkloadLayers_AreRealLayers is the outer half of the attribution
// internal/pkg/workloads now carries.
//
// layertest.Check proves each layer's charts are attributed to THAT layer, and
// internal/pkg/cni does the same for the CNI's chart, which layertest cannot
// see. Neither can tell a layer name that exists from one that does not: a
// typo, or a layer that was renamed, attributes a chart to nothing and every
// per-layer check keeps passing, because none of them runs in a directory that
// matches.
func TestWorkloadLayers_AreRealLayers(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	raw, err := os.ReadFile(filepath.Join(root, "Taskfile.yaml"))
	require.NoError(t, err)

	list := layerList.FindStringSubmatch(string(raw))
	require.NotNil(t, list, "no LAYERS list in the root taskfile")

	walked := strings.Fields(list[1])
	require.NotEmpty(t, walked)

	named := workloads.Layers()
	require.NotEmpty(t, named, "no layer is named in internal/pkg/workloads")

	for _, layer := range named {
		assert.Contains(t, walked, layer,
			"internal/pkg/workloads attributes charts to %q, which LAYERS does not walk", layer)

		info, statErr := os.Stat(filepath.Join(root, "layers", layer))
		require.NoError(t, statErr, layer)
		assert.True(t, info.IsDir(), layer)
	}

	// The reverse is deliberately NOT asserted: 20-network-policy installs no
	// chart, so its absence here is correct. What makes that safe is
	// TestEveryLayer_ChecksItsComponents above — a layer with charts and no
	// layertest.Check would be the gap, and it fails there.
	assert.Less(t, len(named), len(walked),
		"every layer installs a chart now, so the exemption this test documents is stale")
}

// TestLayerList_StartsWithTheNodePlatform holds the one position in LAYERS
// that is not a preference.
//
// The list's own comment says it: a node without a CNI stays NotReady, and a
// NotReady node cannot schedule the cloud controller manager that would clear
// Talos's uninitialized taint — so 10-node-platform is first or nothing above
// it comes up at all. Everything from 30-cluster-services onwards IS a
// preference, and this deliberately says nothing about it.
//
// Until now the order was held by the numbering alone, which is a convention
// rather than a check: renumbering a layer, or inserting one ahead of it,
// reorders `layer=all` with nothing failing until an apply against an empty
// cluster hangs on pods that cannot be scheduled.
func TestLayerList_StartsWithTheNodePlatform(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "Taskfile.yaml"))
	require.NoError(t, err)

	list := layerList.FindStringSubmatch(string(raw))
	require.NotNil(t, list, "no LAYERS list in the root taskfile")

	walked := strings.Fields(list[1])
	require.NotEmpty(t, walked)

	assert.Equal(t, workloads.LayerNodePlatform, walked[0],
		"%s must be the first layer LAYERS walks: it installs the CNI, and nothing above it "+
			"can be scheduled on a node that has none", workloads.LayerNodePlatform)
}
