package ci

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// alertPermission is what a fine-grained token calls the access Renovate needs
// to read vulnerability alerts.
const alertPermission = "Dependabot alerts"

// TestRenovateAlerts_AgreeWithWhatTheTokenIsToldToAllow keeps one setting and
// the instructions for it from drifting apart, in whichever direction.
//
// Measured, and it ran the wrong way for as long as the token existed:
// `renovate.json` configured `vulnerabilityAlerts` and the fine-grained token
// the workflow tells you to create did not include Dependabot alerts. Renovate
// logged
//
//	WARN: Cannot access vulnerability alerts.
//
// once per run and carried on with everything else, so the feature was
// configured, believed, and dead.
//
// It is off now, and the drift is available in the other direction: a
// permission list that still demands the access sends somebody to widen a
// credential for a feature nothing uses. So the check has two arms and the
// configuration picks which one applies.
func TestRenovateAlerts_AgreeWithWhatTheTokenIsToldToAllow(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	raw, err := os.ReadFile(filepath.Join(root, ".github", "renovate.json"))
	require.NoError(t, err)

	var config struct {
		VulnerabilityAlerts *struct {
			Enabled *bool `json:"enabled"`
		} `json:"vulnerabilityAlerts"`
	}
	require.NoError(t, json.Unmarshal(raw, &config))

	// Absent means the default, which is enabled.
	enabled := true
	if config.VulnerabilityAlerts != nil && config.VulnerabilityAlerts.Enabled != nil {
		enabled = *config.VulnerabilityAlerts.Enabled
	}

	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "renovate.yaml"))
	require.NoError(t, err)

	docs, err := os.ReadFile(filepath.Join(root, "docs", "ci.md"))
	require.NoError(t, err)

	// The same shape either way: a line in the list of what to grant. Matching
	// the words alone was the first form, and it passed in the broken state
	// this exists to catch — the explanation "Not `Dependabot alerts`, because
	// …" contains them.
	granted := regexp.MustCompile(`(?m)^\s+` + alertPermission + `\s+read`)

	if enabled {
		assert.Regexp(t, granted, string(workflow),
			"renovate.json asks for vulnerability alerts and renovate.yaml does not tell "+
				"whoever creates the token to allow %q, which is how the feature came to be "+
				"configured and dead", alertPermission)
		assert.Contains(t, string(docs), alertPermission,
			"and docs/ci.md does not mention it either, so nothing a reader opens says what "+
				"the token needs")

		return
	}

	// Off: the instructions may explain the permission, and must not ask for
	// it. "Not `Dependabot alerts`" is an explanation; a line in the list of
	// what to grant is a request.
	assert.NotRegexp(t, granted, string(workflow),
		"vulnerabilityAlerts are off and renovate.yaml still lists %q among the permissions "+
			"to grant, which sends somebody to widen a credential for a feature nothing uses",
		alertPermission)

	assert.Contains(t, string(docs), "off",
		"vulnerabilityAlerts are off and docs/ci.md does not say so, so the documented "+
			"behaviour is a security fast path this repository does not have")
}
