package ci

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// varReference is a template reference to a variable: {{.name}} or {{.name}}
// inside a larger expression.
var varReference = regexp.MustCompile(`\{\{[^}]*\.([A-Za-z_][A-Za-z0-9_]*)`)

// TestTaskfileVars_ReferenceOnlyWhatIsDeclaredAbove holds the order a
// file-level vars block is rendered in.
//
// Task renders a file's vars top to bottom. A variable that references one
// declared further down gets that one rendered without the CLI's variables,
// silently: cluster:etcd:restore built `infra/cluster/cluster..yaml`, because
// _CL_CONTROL_PLANE named _CL_CLUSTER_NAME forty lines before it was declared,
// and the restore stopped at the topology read — before it wiped anything,
// which is the only reason that was cheap to find.
func TestTaskfileVars_ReferenceOnlyWhatIsDeclaredAbove(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	files, err := filepath.Glob(filepath.Join(root, "tasks", "*.task.yaml"))
	require.NoError(t, err)

	files = append(files, filepath.Join(root, "Taskfile.yaml"), filepath.Join(root, devTaskfile))
	require.NotEmpty(t, files)

	for _, file := range files {
		raw, err := os.ReadFile(file)
		require.NoError(t, err)

		var document yaml.Node
		require.NoError(t, yaml.Unmarshal(raw, &document), file)

		vars := mappingValue(document.Content[0], "vars")
		if vars == nil {
			continue
		}

		position := map[string]int{}
		for i := 0; i+1 < len(vars.Content); i += 2 {
			position[vars.Content[i].Value] = i
		}

		for i := 0; i+1 < len(vars.Content); i += 2 {
			name, value := vars.Content[i].Value, vars.Content[i+1]

			for _, match := range varReference.FindAllStringSubmatch(nodeText(value), -1) {
				referenced, declared := position[match[1]]
				if !declared {
					continue // a CLI or built-in variable, such as .stack or .ROOT
				}

				assert.Less(t, referenced, i, "%s: %s references %s, which is declared below it",
					filepath.Base(file), name, match[1])
			}
		}
	}
}

// mappingValue is the value of key in a YAML mapping node, or nil.
func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}

	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}

	return nil
}

// nodeText is every scalar under a node, joined: a var is a string, or a map
// with sh: or ref: whose strings carry the references.
func nodeText(node *yaml.Node) string {
	if node.Kind == yaml.ScalarNode {
		return node.Value
	}

	text := ""
	for _, child := range node.Content {
		text += nodeText(child) + "\n"
	}

	return text
}
