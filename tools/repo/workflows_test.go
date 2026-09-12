package repo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// workflow is the part of a GitHub Actions workflow this test walks.
type workflow struct {
	Jobs map[string]struct {
		Steps []struct {
			Name             string `json:"name"`
			Run              string `json:"run"`
			WorkingDirectory string `json:"working-directory"`
		} `json:"steps"`
	} `json:"jobs"`
}

// TestWorkflows_RunGoToolsFromTheWorkspace catches a relative package path
// that resolves against the wrong directory.
//
// `go run ./tools/stack` is correct in a step that runs at the repository
// root and wrong in one that sets working-directory: under
// infra/cluster it resolves to infra/cluster/tools/stack, and Go's answer is
// `stat .../infra/cluster/tools/stack: directory not found`. It failed the
// drift-detection workflow in exactly that way.
//
// Neither actionlint nor a compiler sees it: the path is a string until the
// step runs, and the step only runs on a schedule. The fix is the taskfiles'
// own idiom — address the tool absolutely, from $GITHUB_WORKSPACE.
func TestWorkflows_RunGoToolsFromTheWorkspace(t *testing.T) {
	t.Parallel()

	dir := filepath.Join("..", "..", ".github", "workflows")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	var steps int

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}

		raw, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
		require.NoError(t, readErr)

		var parsed workflow
		require.NoError(t, yaml.Unmarshal(raw, &parsed), entry.Name())

		for job, spec := range parsed.Jobs {
			for _, step := range spec.Steps {
				if step.WorkingDirectory == "" || !strings.Contains(step.Run, "go run ./") {
					continue
				}

				steps++

				assert.Fail(t,
					"a relative go package path in a step that changed directory",
					"%s job %q step %q runs `go run ./…` from %s, where the package path does not resolve — address it as $GITHUB_WORKSPACE/…",
					entry.Name(), job, step.Name, step.WorkingDirectory)
			}
		}
	}

	assert.Zero(t, steps)
}
