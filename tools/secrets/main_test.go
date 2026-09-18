package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/secretout"
)

func TestRun_RefusesATerminal(t *testing.T) {
	t.Parallel()

	// The refusal is the feature. A certificate authority in scrollback
	// outlives the session, survives into a screen share, and nothing warns
	// about it afterwards.
	err := run(context.Background(), []string{"dev"}, true)

	require.ErrorIs(t, err, secretout.ErrTerminal)
	assert.Contains(t, err.Error(), "pass insert", "the refusal has to name the way through")
	assert.Contains(t, err.Error(), "stack=dev", "the remedy names the stack that was asked for")
}

func TestRun_RejectsTheWrongNumberOfArguments(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"none":     {},
		"too many": {"dev", "extra"},
	} {
		// Checked before the terminal test, so a mistyped invocation says
		// what it wants rather than how to pipe it.
		err := run(context.Background(), args, true)

		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "usage:", name)
	}
}
