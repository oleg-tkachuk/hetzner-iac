package ci

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// releaseConfig is semantic-release's configuration.
	releaseConfig = "release.config.cjs"
	// releasePreset is the conventional-changelog preset both plugins use.
	releasePreset = "conventionalcommits"
	// maxReleasePresetMajor is the newest preset major that renders with
	// conventional-changelog-writer 8, the writer release-notes-generator 14
	// ships. 10.x needs writer 9: 10.2.1 rendered empty notes, 10.4.0 refuses.
	maxReleasePresetMajor = 9
)

// presetPin is the preset version the release workflow installs.
var presetPin = regexp.MustCompile(`conventional-changelog-conventionalcommits@(\d+)\.\d+\.\d+`)

// TestReleaseConfig_BothPluginsReadOnePreset is the gate for a breaking change
// listed without a heading. The analyzer had the preset and the notes
// generator did not, so the generator fell back to angular, which does not
// parse `type!:`.
func TestReleaseConfig_BothPluginsReadOnePreset(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", releaseConfig))
	require.NoError(t, err)

	for _, plugin := range []string{"commit-analyzer", "release-notes-generator"} {
		configured := regexp.MustCompile(`\["@semantic-release/` + plugin + `",\s*\{\s*preset:\s*"` + releasePreset + `"`)
		assert.Regexp(t, configured, string(raw), "%s does not use the %s preset", plugin, releasePreset)
	}
}

func TestReleaseWorkflow_PinsAPresetTheWriterCanRender(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", releaseWorkflow))
	require.NoError(t, err)

	pin := presetPin.FindStringSubmatch(string(raw))
	require.NotNil(t, pin, "%s installs no %s preset", releaseWorkflow, releasePreset)

	major, err := strconv.Atoi(pin[1])
	require.NoError(t, err)
	assert.LessOrEqual(t, major, maxReleasePresetMajor,
		"preset %s.x needs a newer conventional-changelog-writer than release-notes-generator ships, "+
			"and the release notes come out empty", pin[1])
}
