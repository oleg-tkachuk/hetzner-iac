package ci

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hourConstrained matches a Renovate schedule that narrows the time of day —
// the `before`/`after` forms, and a cron whose hour field is not `*`.
var hourConstrained = regexp.MustCompile(`(?i)\b(before|after)\b|^\S+\s+[^*\s]`)

// TestRenovateScheduleIsNotNarrowerThanADay refuses a schedule that only a
// punctual cron could satisfy.
//
// Renovate runs here from a GitHub scheduled workflow, and GitHub runs those
// best-effort on shared runners. The schedule was `before 09:00 on monday`,
// which with `timezone: Europe/Kyiv` is Sunday 21:00 to Monday 06:00 UTC, while
// the workflow's cron fires at 06:00 UTC — exactly as that window shuts. The
// Monday pass started at 11:49 UTC, five hours and forty-nine
// minutes late, found three updates, and filed all three under "Awaiting
// Schedule". Renovate had opened no pull request in this repository, ever.
//
// Nothing reported it. `renovate-config-validator` accepts the broken schedule
// and the working one identically — checked, all four candidate forms pass — so
// validation cannot be the thing that catches this.
//
// A day-wide window still batches updates into one weekly review, which is the
// only thing the narrow one was for. What it drops is the dependency on a cron
// being punctual.
func TestRenovateScheduleIsNotNarrowerThanADay(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "renovate.json"))
	require.NoError(t, err)

	var config struct {
		Schedule []string `json:"schedule"`
	}

	require.NoError(t, json.Unmarshal(raw, &config))
	require.NotEmpty(t, config.Schedule, "no schedule in renovate.json")

	for _, entry := range config.Schedule {
		assert.False(t, hourConstrained.MatchString(strings.TrimSpace(entry)),
			"renovate.json schedule %q narrows the time of day. Renovate is triggered by a "+
				"GitHub cron, which is best-effort and was observed 5h49m late — a window "+
				"of hours is a window it misses. Keep the day and drop the hour.", entry)
	}
}

// TestRenovateCronIsDailyForSecurityFixes holds the other half of the pair.
//
// The daily cron is not redundant with a weekly schedule: `vulnerabilityAlerts`
// is exempt from the schedule, so a security fix can land on any morning's pass
// while ordinary updates still batch into Monday. A weekly cron would delay a
// security fix by up to seven days, which is the one thing self-hosting must
// not cost — and it is stated in the workflow beside the cron.
func TestRenovateCronIsDailyForSecurityFixes(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "renovate.yaml"))
	require.NoError(t, err)

	cron := regexp.MustCompile(`(?m)^\s*-\s*cron:\s*"([^"]+)"`).FindStringSubmatch(string(raw))
	require.Len(t, cron, 2, "no cron in the renovate workflow")

	fields := strings.Fields(cron[1])
	require.Len(t, fields, 5, "cron %q is not five fields", cron[1])

	// Day-of-week and day-of-month both unconstrained: it runs every day.
	assert.Equal(t, "*", fields[4],
		"cron %q is not daily; vulnerabilityAlerts would then wait for it", cron[1])
	assert.Equal(t, "*", fields[2],
		"cron %q is not daily; vulnerabilityAlerts would then wait for it", cron[1])
}
