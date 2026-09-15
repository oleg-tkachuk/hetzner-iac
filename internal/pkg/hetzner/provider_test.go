package hetzner_test

import (
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/hetzner"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewProvider_CarriesTheTokenAsASecret is the whole behaviour worth
// pinning: the provider is built from the token the cluster tier publishes,
// and that token stays marked.
//
// The provider's own inputs land in state like any other resource's, so a
// token that arrives unmarked is a credential in plaintext in state and in
// every preview diff.
func TestNewProvider_CarriesTheTokenAsASecret(t *testing.T) {
	t.Parallel()

	rec := newRecorder()

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		provider, err := hetzner.NewProvider(ctx, pulumi.ToSecret(pulumi.String("a-token")).(pulumi.StringOutput))
		require.NotNil(t, provider)

		return err
	}, pulumi.WithMocks("hetzner-iac", "test", rec))

	require.NoError(t, err)

	registered := rec.of("pulumi:providers:hcloud")
	require.Len(t, registered, 1)

	token := registered[0]["token"]
	require.True(t, token.IsSecret(), "the provider's token is not marked secret")

	assert.Equal(t, "a-token", token.SecretValue().Element.StringValue())
}

// TestNewProvider_NameIsTheOneEveryLayerShares holds the resource name still.
//
// It is part of the provider's URN, so changing it replaces the provider — and
// replacing a provider replaces every resource created with it. On the backup
// layer that is a delete-protected Storage Box, where the replace is refused
// and the stack is left half-applied.
func TestNewProvider_NameIsTheOneEveryLayerShares(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "hcloud", hetzner.ProviderName)

	rec := newRecorder()

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := hetzner.NewProvider(ctx, pulumi.String("a-token"))

		return err
	}, pulumi.WithMocks("hetzner-iac", "test", rec))

	require.NoError(t, err)

	require.Len(t, rec.of("pulumi:providers:hcloud"), 1,
		"the provider is not registered under the hcloud package")
}
