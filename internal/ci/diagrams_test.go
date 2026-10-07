package ci

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/charts"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A diagram is a claim about the system that nothing rebuilds, so it rots in
// silence — the one kind of documentation this repository has no answer for.
// These tests are the answer: everything a diagram says that names a real
// thing is held against the thing.
//
// Layer names are already covered elsewhere.
// TestEveryLayerReference_PointsAtADirectoryThatExists walks the whole tree,
// so a diagram naming a removed layer — 60-observability, say — fails there.
// It caught this file's first draft, which named one in this very comment.
// What it cannot catch is a layer left OUT of a diagram, or a name that is
// real but wrong in context, which is what the tests below are for.
//
// The name above is deliberately written without the path prefix, for the
// same reason that test writes its own the same way: it reads its own source
// like any other file.

// layerDirectories is the layers in dependency order, from LAYERS in the root
// taskfile — the one list the apply loop walks. TestLayerEnum_MatchesTheLayerList
// holds it equal to the enum, and TestEveryLayerReference_PointsAtADirectoryThatExists
// holds its names equal to the directories, so a diagram checked against it is
// checked against the same source the runtime uses.
func layerDirectories(t *testing.T) []string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("..", "..", "Taskfile.yaml"))
	require.NoError(t, err)

	list := layerList.FindStringSubmatch(string(raw))
	require.NotNil(t, list, "no LAYERS list in the root taskfile")

	layers := strings.Fields(list[1])
	require.NotEmpty(t, layers)

	return layers
}

// mermaidBlock matches one fenced mermaid block.
var mermaidBlock = regexp.MustCompile("(?s)```mermaid\n(.*?)\n```")

// diagrams returns every mermaid block in the documentation, keyed by nothing:
// the assertions below are about the set of diagrams, not about which file a
// given one sits in.
func diagrams(t *testing.T) []string {
	t.Helper()

	paths, err := filepath.Glob(filepath.Join("..", "..", "docs", "*.md"))
	require.NoError(t, err)

	paths = append(paths, filepath.Join("..", "..", "README.md"))

	var blocks []string

	for _, path := range paths {
		// BACKLOG.md is planning and gitignored; on a clean clone the glob
		// never finds it.
		if filepath.Base(path) == "BACKLOG.md" {
			continue
		}

		raw, err := os.ReadFile(path)
		require.NoError(t, err)

		for _, found := range mermaidBlock.FindAllStringSubmatch(string(raw), -1) {
			blocks = append(blocks, found[1])
		}
	}

	require.NotEmpty(t, blocks, "no mermaid diagrams found — this file is checking nothing")

	return blocks
}

// containmentDiagram is the one that shows what lives inside what. Found by a
// string only it contains, rather than by position in the file.
func diagramContaining(t *testing.T, marker string) string {
	t.Helper()

	for _, block := range diagrams(t) {
		if strings.Contains(block, marker) {
			return block
		}
	}

	require.FailNowf(t, "no diagram found", "no diagram contains %q", marker)

	return ""
}

// TestContainmentDiagram_PlacesEveryLayer catches the layer a diagram leaves
// out, which reads as a layer that does not exist.
func TestContainmentDiagram_PlacesEveryLayer(t *testing.T) {
	t.Parallel()

	diagram := diagramContaining(t, "Hetzner Cloud project")

	for _, layer := range layerDirectories(t) {
		assert.Contains(t, diagram, "layers/"+layer,
			"the containment diagram places no %s, so a reader cannot tell where it writes", layer)
	}
}

// TestContainmentDiagram_NamesNamespacesThatChartsInstallInto holds the
// diagram to the registry. A namespace named here that no chart installs into
// sends a reader to `kubectl -n` for something empty.
func TestContainmentDiagram_NamesNamespacesThatChartsInstallInto(t *testing.T) {
	t.Parallel()

	diagram := diagramContaining(t, "Hetzner Cloud project")

	installed := map[string]bool{}
	for _, chart := range charts.All() {
		installed[chart.Namespace] = true
	}

	require.NotEmpty(t, installed, "no charts in the registry — this test is checking nothing")

	var checked int

	for namespace := range installed {
		if !strings.Contains(diagram, namespace) {
			continue
		}

		checked++
	}

	assert.Equal(t, len(installed), checked,
		"the diagram names %d of the %d namespaces charts install into; the missing ones are invisible to a reader",
		checked, len(installed))
}

// TestRequestPathDiagram_UsesTheEntryPointNamesTraefikIsGiven keeps the
// diagram spelling the entry points the layer actually configures. Renaming
// one in a chart's own file and not here leaves a diagram describing a
// Traefik nobody deployed.
func TestRequestPathDiagram_UsesTheEntryPointNamesTraefikIsGiven(t *testing.T) {
	t.Parallel()

	diagram := diagramContaining(t, "PROXY header")

	for _, entryPoint := range []string{
		charts.TraefikEntryPointWeb,
		charts.TraefikEntryPointTLS,
	} {
		assert.Contains(t, diagram, entryPoint,
			"the request path does not name the %q entry point, and forgetting one breaks only half the traffic",
			entryPoint)
	}
}

// TestRequestPathDiagram_ShowsThePinnedNodePorts pins the numbers in the
// diagram that are a contract between two programs.
//
// It used to pin the cloud controller manager's `use-private-ip` annotation.
// That annotation is gone: the load balancer is Pulumi's now, and the contract
// that replaced it is the pair of node ports — internal/pkg/platform names them,
// internal/pkg/hetzner points the load balancer at them, and the values template asks
// Kubernetes for them. A diagram showing a port nothing forwards to is the
// same class of wrong the annotation test existed for.
func TestRequestPathDiagram_ShowsThePinnedNodePorts(t *testing.T) {
	t.Parallel()

	diagram := diagramContaining(t, "PROXY header")

	// Asked of the package that owns the template, rather than of a path: the
	// templates live beside the chart that names them now.
	template, err := charts.Source(charts.Traefik)
	require.NoError(t, err)

	// The template interpolates them rather than spelling them, which is the
	// point — so it is checked for the placeholder, and the numbers are
	// checked against the constants both sides read.
	assert.Contains(t, template, "{{ .NodePortHTTP }}",
		"the values template no longer asks for a pinned node port")

	for _, port := range []int{platform.IngressNodePortHTTP, platform.IngressNodePortHTTPS} {
		assert.Contains(t, diagram, strconv.Itoa(port),
			"the request path does not show node port %d, which is what the load balancer "+
				"forwards to", port)
	}
}

// TestHandoverDiagram_NamesTheLayerThatEndsNotReady is the claim worth
// pinning in the sequence: the CNI comes from the FIRST layer, and the
// NotReady window lasts exactly until it runs. Reorder the layers and this
// diagram describes a cluster that no longer exists.
func TestHandoverDiagram_NamesTheLayerThatEndsNotReady(t *testing.T) {
	t.Parallel()

	diagram := diagramContaining(t, "NotReady")

	first := layerDirectories(t)[0]

	assert.Contains(t, diagram, "layers/"+first+" installs the CNI",
		"the handover diagram credits the CNI to another layer than the first one applied")

	// And the note has to say WHOSE absence it describes. Worded as "no CNI is
	// installed here" it was read as a standing fact about the cluster, which
	// is the opposite of what the diagram means — Cilium is installed, one
	// step later.
	assert.Contains(t, diagram, "the cluster tier installs no CNI",
		"the NotReady note does not name the tier, so it reads as a claim that this platform has no CNI")
}

// diagramPalette is the one set of classes every flowchart declares, byte for
// byte. The colour says whose territory a box is in, which only works if it
// means the same thing in every diagram; five diagrams with five palettes of
// their own had green meaning Hetzner in one and "the edge" in the next.
//
// Light fills with dark text of the same hue, spelled out: GitHub renders a
// diagram in both themes, and a colour left to the theme is unreadable in one
// of them. The README states the legend once.
var diagramPalette = []string{
	"classDef actor fill:#E0F2FE,stroke:#0284C7,color:#0C4A6E",
	"classDef optional fill:#F1F5F9,stroke:#64748B,color:#334155,stroke-dasharray:5 4",
	"classDef hetzner fill:#DCFCE7,stroke:#16A34A,color:#14532D",
	"classDef talos fill:#FEF3C7,stroke:#D97706,color:#78350F",
	"classDef kube fill:#DBEAFE,stroke:#2563EB,color:#1E3A8A",
	"classDef state fill:#EDE9FE,stroke:#7C3AED,color:#3B0764",
	"classDef gate fill:#FCE7F3,stroke:#DB2777,color:#831843",
}

// flowchartPrefix opens a flowchart; the palette is a flowchart's concern, and
// a sequence diagram has no classes to hold to it.
const flowchartPrefix = "flowchart "

// classAssignment matches `class a,b name`, capturing the ids and the class.
var classAssignment = regexp.MustCompile(`(?m)^\s*class (\S+) (\S+)\s*$`)

func flowcharts(t *testing.T) []string {
	t.Helper()

	var found []string

	for _, block := range diagrams(t) {
		if strings.HasPrefix(block, flowchartPrefix) {
			found = append(found, block)
		}
	}

	require.NotEmpty(t, found, "no flowcharts found — the palette tests are checking nothing")

	return found
}

// TestEveryFlowchart_DeclaresExactlyTheSharedPalette catches the diagram that
// grows a colour of its own, or drops one and so renders a box unstyled.
func TestEveryFlowchart_DeclaresExactlyTheSharedPalette(t *testing.T) {
	t.Parallel()

	for _, block := range flowcharts(t) {
		var declared []string

		for _, line := range strings.Split(block, "\n") {
			if line = strings.TrimSpace(line); strings.HasPrefix(line, "classDef ") {
				declared = append(declared, line)
			}
		}

		assert.Equal(t, diagramPalette, declared,
			"a flowchart declares another palette than the shared one:\n%s", block)
	}
}

// TestEveryFlowchart_AssignsOnlyPaletteClasses catches a class name the
// palette does not define. Mermaid ignores it without a word, and the box
// renders in the theme's colours — the one thing the palette exists to stop.
func TestEveryFlowchart_AssignsOnlyPaletteClasses(t *testing.T) {
	t.Parallel()

	known := map[string]bool{}

	for _, line := range diagramPalette {
		known[strings.Fields(line)[1]] = true
	}

	for _, block := range flowcharts(t) {
		for _, found := range classAssignment.FindAllStringSubmatch(block, -1) {
			assert.True(t, known[found[2]],
				"a flowchart assigns class %q, which the palette does not define", found[2])
		}
	}
}

// stadiumNode matches a node drawn as a stadium, `id(["…"])`, capturing its id.
var stadiumNode = regexp.MustCompile(`(?m)^\s*(\w+)\(\[`)

// actorClass is the palette class a stadium has to carry.
const actorClass = "actor"

// TestEveryStadium_IsAnActor holds the shape grammar the README's legend
// states: a rounded box is a person. One drawn round for looks reads as
// somebody who acts on the system.
func TestEveryStadium_IsAnActor(t *testing.T) {
	t.Parallel()

	var checked int

	for _, block := range flowcharts(t) {
		actors := map[string]bool{}

		for _, found := range classAssignment.FindAllStringSubmatch(block, -1) {
			if found[2] != actorClass {
				continue
			}

			for _, id := range strings.Split(found[1], ",") {
				actors[id] = true
			}
		}

		for _, found := range stadiumNode.FindAllStringSubmatch(block, -1) {
			checked++

			assert.True(t, actors[found[1]],
				"%s is drawn as a stadium, which the legend reserves for people, but is not of class %s",
				found[1], actorClass)
		}
	}

	require.NotZero(t, checked, "no stadiums found — this test is checking nothing")
}

// TestNoDiagram_CarriesAnEmoji keeps labels to text. An emoji — any symbol
// of Unicode's "other symbol" category — renders as a different glyph on every
// platform, and the shapes already say what one would.
func TestNoDiagram_CarriesAnEmoji(t *testing.T) {
	t.Parallel()

	for _, block := range diagrams(t) {
		for _, r := range block {
			assert.False(t, unicode.Is(unicode.So, r),
				"a diagram carries %q:\n%s", string(r), block)
		}
	}
}
