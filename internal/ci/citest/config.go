package citest

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// DeclaredKey matches one entry in a Pulumi.yaml `config:` block — two leading
// spaces, `project:key`, and nothing else on the line.
var DeclaredKey = regexp.MustCompile(`(?m)^  ([a-z-]+):(\w+):$`)

// DeliberatelyUndeclared are keys a layer reads and its Pulumi.yaml does not
// declare, on purpose, with the reason written in that Pulumi.yaml. The value
// is text that must still be there: an exception whose reason has been deleted
// is no longer an exception.
var DeliberatelyUndeclared = map[string]string{
	"node-platform:hcloudToken": "deliberately NOT declared",
	"ingress:hcloudToken":       "deliberately NOT declared",
}

// projectRoots are the directories whose children are Pulumi projects that
// read stack config: the layers, and the tiers under infra/.
//
// The tiers were missing, and it cost the gate its reach without a word: the
// backup tier moved from layers/ to infra/, and from then on nothing checked
// that the keys it reads are declared, or that the tables documenting them
// name keys that exist — every `backup:` row was skipped as "not a layer".
var projectRoots = []string{"layers", "infra"}

// projectName matches the `name:` line of a Pulumi.yaml.
var projectName = regexp.MustCompile(`(?m)^name: (\S+)$`)

// LayerProjects returns each project's Pulumi project name and the directory
// it lives in, read from the Pulumi.yaml rather than assumed from the directory
// — the two differ on purpose (`layers/40-ingress` is project `ingress`).
func LayerProjects(t *testing.T, root string) map[string]string {
	t.Helper()

	projects := map[string]string{}

	for _, parent := range projectRoots {
		entries, err := os.ReadDir(filepath.Join(root, parent))
		require.NoError(t, err)

		var found int

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}

			dir := filepath.Join(root, parent, entry.Name())

			// #nosec G304 -- a committed Pulumi.yaml under the repository root;
			// reading the project files is what this function is for.
			project, err := os.ReadFile(filepath.Join(dir, "Pulumi.yaml"))
			require.NoError(t, err)

			match := projectName.FindSubmatch(project)
			require.NotNil(t, match, "%s/%s has no `name:` in its Pulumi.yaml", parent, entry.Name())

			projects[string(match[1])] = dir
			found++
		}

		require.Positive(t, found, "no projects under %s/; this test is not reaching it", parent)
	}

	return projects
}
