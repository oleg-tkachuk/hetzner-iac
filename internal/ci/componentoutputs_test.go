package ci

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	// outputTag is the name a component field publishes under.
	outputTag = regexp.MustCompile(`pulumi:"([a-zA-Z]+)"`)

	// registeredOutput is a key in a RegisterResourceOutputs map. Every one of
	// them takes its value from the component, which is what makes the match
	// this narrow: a registration written some other way is reported as a
	// missing key rather than passing unread.
	registeredOutput = regexp.MustCompile(`^\s*"([a-zA-Z]+)":\s+component\.`)
)

// TestComponentOutputs_AreTheFieldsTheyAreTaggedAs pairs the two halves of a
// component resource's output contract.
//
// A field's `pulumi:"…"` tag is the name it publishes under; the map handed to
// RegisterResourceOutputs is what actually gets published. Nothing in Go ties
// them together — a tag cannot be a constant, the language forbids anything
// but a literal there — so a field added to the struct and forgotten in the
// registration is an output that exists in the type and not in the state, and
// the compiler has nothing to say about it.
func TestComponentOutputs_AreTheFieldsTheyAreTaggedAs(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..", "internal", "pkg", "hetzner")

	entries, err := os.ReadDir(root)
	require.NoError(t, err)

	var checked int

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		path := filepath.Join(root, name)

		raw, readErr := os.ReadFile(path)
		require.NoError(t, readErr, path)

		var tagged, registered []string

		for _, match := range outputTag.FindAllStringSubmatch(string(raw), -1) {
			tagged = append(tagged, match[1])
		}

		for _, line := range strings.Split(string(raw), "\n") {
			if match := registeredOutput.FindStringSubmatch(line); match != nil {
				registered = append(registered, match[1])
			}
		}

		// A component with no outputs registers none, which is its own
		// consistent answer.
		if len(tagged) == 0 && len(registered) == 0 {
			continue
		}

		slices.Sort(tagged)
		slices.Sort(registered)

		tagged = slices.Compact(tagged)
		registered = slices.Compact(registered)

		assert.Equal(t, tagged, registered,
			"%s declares outputs %v and registers %v", relativeToRoot(path), tagged, registered)

		checked++
	}

	assert.Positive(t, checked, "no component registers an output; this test is checking nothing")
}
