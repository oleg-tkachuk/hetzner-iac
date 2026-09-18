package secretout_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/secretout"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRefuse_CarriesTheSentinelSoACallerCanMatchIt is what the two commands'
// own tests depend on: they assert the refusal, not its wording.
func TestRefuse_CarriesTheSentinelSoACallerCanMatchIt(t *testing.T) {
	t.Parallel()

	err := secretout.Refuse("the cluster's recovery kit",
		"task cluster:recovery-kit stack=prod", "hetzner/prod/recovery-kit")

	require.ErrorIs(t, err, secretout.ErrTerminal)
	assert.ErrorIs(t, fmt.Errorf("wrapped: %w", err), secretout.ErrTerminal,
		"the sentinel has to survive another layer of wrapping")
}

// TestRefuse_NamesWhatIsWithheldAndTheCommandThatKeepsIt holds the part that is
// the point of the message.
//
// The reader has just typed a command and has nothing else in front of them. A
// refusal that only says no sends them to the documentation; this one is
// something they can run.
func TestRefuse_NamesWhatIsWithheldAndTheCommandThatKeepsIt(t *testing.T) {
	t.Parallel()

	err := secretout.Refuse("the cluster's certificate authority",
		"task cluster:secrets:export stack=prod", "hetzner/prod/talos-secrets")

	message := err.Error()

	for _, part := range []string{
		"the cluster's certificate authority",
		"task cluster:secrets:export stack=prod",
		"pass insert -m hetzner/prod/talos-secrets",
	} {
		assert.Contains(t, message, part)
	}

	// On its own line, because it is meant to be copied.
	lines := strings.Split(message, "\n")
	require.Greater(t, len(lines), 2, "the command shares a line with the prose: %q", message)
	assert.Equal(t, "  task cluster:secrets:export stack=prod | pass insert -m hetzner/prod/talos-secrets",
		lines[len(lines)-1])
}

// TestErrTerminal_SaysNothingAboutOneCommand keeps the sentinel reusable: the
// two callers name their own material, and a third would too.
func TestErrTerminal_SaysNothingAboutOneCommand(t *testing.T) {
	t.Parallel()

	message := secretout.ErrTerminal.Error()

	assert.NotContains(t, message, "recovery kit")
	assert.NotContains(t, message, "certificate authority")
	assert.True(t, errors.Is(secretout.ErrTerminal, secretout.ErrTerminal))
}
