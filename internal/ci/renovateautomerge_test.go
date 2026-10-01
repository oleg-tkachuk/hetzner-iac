package ci

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mergeStrategyRebase is the only merge method the repository allows; any
// other makes GitHub refuse to enable auto-merge on the pull request.
const mergeStrategyRebase = "rebase"

// TestRenovate_EveryPullRequestMergesItself pins that Renovate's pull requests
// are handed to GitHub's auto-merge, and that no rule opts one back out.
func TestRenovate_EveryPullRequestMergesItself(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "renovate.json"))
	require.NoError(t, err)

	var config struct {
		Automerge         *bool  `json:"automerge"`
		PlatformAutomerge *bool  `json:"platformAutomerge"`
		AutomergeStrategy string `json:"automergeStrategy"`
		PackageRules      []struct {
			Description string `json:"description"`
			Automerge   *bool  `json:"automerge"`
		} `json:"packageRules"`
	}
	require.NoError(t, json.Unmarshal(raw, &config))

	require.NotNil(t, config.Automerge, "automerge is unset, and Renovate's default is off")
	assert.True(t, *config.Automerge)

	require.NotNil(t, config.PlatformAutomerge)
	assert.True(t, *config.PlatformAutomerge,
		"without platform auto-merge Renovate merges only when it next runs, a day later")

	assert.Equal(t, mergeStrategyRebase, config.AutomergeStrategy)

	for _, rule := range config.PackageRules {
		if rule.Automerge != nil {
			assert.True(t, *rule.Automerge, "a rule turns automerge off: %s", rule.Description)
		}
	}
}
