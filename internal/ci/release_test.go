package ci

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// releaseWorkflow is the file semantic-release runs from.
const releaseWorkflow = ".github/workflows/release.yaml"

// releaseAction is the step that tags and publishes, matched by its action
// rather than its name, which carries no meaning to GitHub.
const releaseAction = "cycjimmy/semantic-release-action@"

// TestRelease_PublishesOnlyTheCommitCIPassed holds the gate the release relies
// on to be about the right commit.
//
// CI dispatches the release for the sha it just passed, and the release checks
// out main's head — deliberately, since semantic-release needs a branch. A
// merge landing in between was tagged and released from a commit whose CI was
// still running, which is the v1.0.2 failure this file's header says the
// dispatch design ended. The publishing step now runs only when main is still
// at the dispatched sha, and one release runs at a time.
func TestRelease_PublishesOnlyTheCommitCIPassed(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", releaseWorkflow))
	require.NoError(t, err)

	var parsed struct {
		Concurrency struct {
			Group            string `json:"group"`
			CancelInProgress bool   `json:"cancel-in-progress"`
		} `json:"concurrency"`
		Jobs map[string]struct {
			Steps []struct {
				ID   string            `json:"id"`
				If   string            `json:"if"`
				Uses string            `json:"uses"`
				Run  string            `json:"run"`
				Env  map[string]string `json:"env"`
			} `json:"steps"`
		} `json:"jobs"`
	}

	require.NoError(t, yaml.Unmarshal(raw, &parsed))

	assert.NotEmpty(t, parsed.Concurrency.Group, "two releases can run at once")
	assert.False(t, parsed.Concurrency.CancelInProgress, "a release half-done must not be cancelled by the next")

	var (
		checkID  string
		releases int
	)

	for _, job := range parsed.Jobs {
		for _, step := range job.Steps {
			if strings.Contains(step.Run, "git rev-parse HEAD") && strings.Contains(step.Env["SHA"], "client_payload.sha") {
				checkID = step.ID
			}

			if !strings.HasPrefix(step.Uses, releaseAction) {
				continue
			}

			releases++

			require.NotEmpty(t, checkID, "the release step runs before anything compares main with the dispatched sha")
			assert.Contains(t, step.If, "steps."+checkID+".outputs",
				"the release step does not wait on the comparison, so it tags whatever main is at")
		}
	}

	assert.Equal(t, 1, releases, "expected exactly one publishing step in %s", releaseWorkflow)
}
