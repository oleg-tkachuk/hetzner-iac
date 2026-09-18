package hcloudtoken_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/hcloudtoken"
)

func TestToken_PrefersTheExportedOne(t *testing.T) {
	// The case that matters for CI and for a second project's token: an
	// exported value must win without pulumi being consulted at all, which is
	// what makes this testable without a stack.
	t.Setenv(hcloudtoken.TokenEnv, "exported-token")

	got, err := hcloudtoken.Token(t.Context(), "dev")

	require.NoError(t, err)
	assert.Equal(t, "exported-token", got)
}

func TestToken_TrimsTheExportedValue(t *testing.T) {
	// A token pasted with a trailing newline authenticates nothing, and the
	// API's answer for it is a 401 that reads like a wrong token rather than
	// a badly copied one.
	t.Setenv(hcloudtoken.TokenEnv, "  exported-token\n")

	got, err := hcloudtoken.Token(t.Context(), "dev")

	require.NoError(t, err)
	assert.Equal(t, "exported-token", got)
}

func TestToken_RefusesWithNothingToReadFrom(t *testing.T) {
	// No exported token and no stack is a caller mistake, not a missing
	// credential — and saying so beats a pulumi error about stack "".
	t.Setenv(hcloudtoken.TokenEnv, "")

	_, err := hcloudtoken.Token(t.Context(), "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no stack")
}
