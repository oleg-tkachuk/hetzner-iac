package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/secretout"
)

func TestRun_RejectsTheWrongNumberOfArguments(t *testing.T) {
	t.Parallel()

	// One argument, because the stack is the only thing that selects a token.
	// Called with none, the underlying resolution would read an exported
	// token and print it for a stack nobody named.
	for name, args := range map[string][]string{
		"none":     {},
		"too many": {"dev", "extra"},
	} {
		err := run(args, false)

		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "usage:", name)
	}
}

func TestRun_PrintsTheExportedToken(t *testing.T) {
	// Through the real resolution, which prefers the environment — so this
	// exercises the whole path without a stack or a network.
	t.Setenv("HCLOUD_TOKEN", "exported-token")

	require.NoError(t, run([]string{"dev"}, false))
}

func TestRun_TheUsageErrorCarriesTheRemedy(t *testing.T) {
	t.Parallel()

	// The shared hcloud module resolves the token through this before any
	// check of its own, so this message is what an operator who forgot
	// stack= actually reads.
	for name, args := range map[string][]string{
		"no arguments": {},
		"empty stack":  {""},
	} {
		err := run(args, false)

		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "stack=dev", name)
	}
}

// TestRun_RefusesATerminal keeps the token out of scrollback.
//
// Its caller is `export HCLOUD_TOKEN="$(…)"`, which is a pipe. Run bare, it
// printed a token that can create and delete every server in the project onto
// the screen, where it outlives the session — the reason tools/secrets and
// tools/recoverykit refuse a terminal, and this said it followed them.
func TestRun_RefusesATerminal(t *testing.T) {
	t.Setenv("HCLOUD_TOKEN", "exported-token")

	err := run([]string{"dev"}, true)

	require.ErrorIs(t, err, secretout.ErrTerminal)
	assert.Contains(t, err.Error(), `export HCLOUD_TOKEN="$(`,
		"the refusal carries the use that keeps it off the screen")
}
