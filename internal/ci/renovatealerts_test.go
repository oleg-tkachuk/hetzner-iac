package ci

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// alertPermission is what a fine-grained token calls the access Renovate needs
// to read vulnerability alerts.
const alertPermission = "Dependabot alerts"

// TestRenovateAlerts_ArePairedWithThePermissionTheyNeed holds a configured
// feature to the access that makes it work.
//
// Measured, and it ran this way for as long as the token existed:
// `renovate.json` configures `vulnerabilityAlerts` — a security fix ignores
// the schedule and the concurrency limits — and the token the workflow
// documents did not include Dependabot alerts. Renovate logged
//
//	WARN: Cannot access vulnerability alerts.
//
// once per run and carried on with everything else, so the feature was
// configured, believed, and dead. The only sign was a line in a log, and the
// dashboard issue repeating it.
//
// The repository setting is the other half and cannot be checked from here.
func TestRenovateAlerts_ArePairedWithThePermissionTheyNeed(t *testing.T) {
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

	// Absent means the default, which is enabled; present and enabled=false is
	// the only shape that asks for nothing.
	if config.VulnerabilityAlerts != nil && config.VulnerabilityAlerts.Enabled != nil &&
		!*config.VulnerabilityAlerts.Enabled {
		t.Skip("renovate.json disables vulnerabilityAlerts, so no permission is needed")
	}

	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "renovate.yaml"))
	require.NoError(t, err)

	assert.Contains(t, string(workflow), alertPermission,
		"renovate.json asks for vulnerability alerts and %s does not tell whoever creates the "+
			"token to allow %q, which is how the feature came to be configured and dead",
		"renovate.yaml", alertPermission)

	docs, err := os.ReadFile(filepath.Join(root, "docs", "ci.md"))
	require.NoError(t, err)

	assert.True(t, strings.Contains(string(docs), alertPermission),
		"and docs/ci.md does not mention it either, so nothing a reader opens says what the "+
			"token needs")
}
