package ci

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The two dependency-bot configurations GitHub acts on. Named with a Path
// suffix because renovate_test.go already uses renovateConfig for the struct
// it parses that file into.
const (
	dependabotConfigPath = ".github/dependabot.yml"
	renovateConfigPath   = ".github/renovate.json"
)

// TestOnlyOneBotUpdatesDependencies keeps two bots off the same ecosystems.
//
// Both were configured for `gomod` and `github-actions` — Renovate through
// config:recommended, Dependabot explicitly — which is two pull requests for
// one upgrade, and two answers to "which bot owns this". It barely showed
// while the repository was private and one person read every notification.
//
// Renovate is the one kept, for a reason Dependabot cannot match: the chart
// pins in internal/pkg/charts are a Go table, and only a custom manager reads
// versions out of it. Dependabot has no equivalent.
//
// This does NOT turn off Dependabot's security updates. Those come from the
// repository's own settings and the advisory database rather than from this
// file, so removing it costs no alerts.
func TestOnlyOneBotUpdatesDependencies(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	_, renovateErr := os.Stat(filepath.Join(root, renovateConfigPath))
	require.NoError(t, renovateErr, "%s is the one kept, and it is missing", renovateConfigPath)

	_, dependabotErr := os.Stat(filepath.Join(root, dependabotConfigPath))
	assert.True(t, os.IsNotExist(dependabotErr),
		"%s is back alongside %s. Both cover gomod and github-actions, so every upgrade "+
			"arrives twice; if Dependabot is now the one wanted, take Renovate out instead",
		dependabotConfigPath, renovateConfigPath)
}
