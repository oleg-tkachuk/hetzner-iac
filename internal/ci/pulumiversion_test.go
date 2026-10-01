package ci

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/plugin"
	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// minimumPulumiCLI is the release that added `--ignore-protect`, which the
	// cluster and backup tasks pass to up, preview and destroy.
	minimumPulumiCLI = "3.256.0"
	// olderPulumiCLI is the release just before it.
	olderPulumiCLI = "3.255.0"
)

// TestPulumiProjects_RequireTheCLITheTasksNeed holds every project to one
// requiredPulumiVersion that admits the CLI the tasks need and refuses the
// one before. Without it an older CLI fails on an unknown flag, part-way
// through a task, instead of before it starts.
func TestPulumiProjects_RequireTheCLITheTasksNeed(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	out, err := exec.CommandContext(t.Context(), "git", "-C", root, "ls-files", "*Pulumi.yaml").Output()
	require.NoError(t, err)

	files := strings.Fields(string(out))
	require.NotEmpty(t, files)

	for _, file := range files {
		project, loadErr := workspace.LoadProject(filepath.Join(root, file))
		require.NoError(t, loadErr, file)

		required := project.RequiredPulumiVersion
		require.NotEmpty(t, required, "%s does not declare requiredPulumiVersion", file)

		// The same validator the CLI runs before a preview or an update.
		require.NoError(t, plugin.ValidatePulumiVersionRange(required, minimumPulumiCLI), file)
		assert.Error(t, plugin.ValidatePulumiVersionRange(required, olderPulumiCLI),
			"%s admits a CLI without --ignore-protect", file)
	}
}
