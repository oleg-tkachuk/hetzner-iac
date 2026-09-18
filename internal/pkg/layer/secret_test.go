package layer_test

import (
	"encoding/base64"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/layer"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/internals"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBase64Of_EncodesWhatKubernetesWillDecode holds the encoding to the one
// the API server applies to a Secret's `data`, which is standard base64 with
// padding — not the URL alphabet and not the raw form.
func TestBase64Of_EncodesWhatKubernetesWillDecode(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"an ordinary token":                     "hcloud-token-value",
		"one whose length needs padding":        "ab",
		"one with bytes the URL alphabet moves": "\xfb\xff?~",
		"an empty one":                          "",
	}

	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			encoded := resolve(t, layer.Base64Of(pulumi.String(raw)))

			assert.Equal(t, base64.StdEncoding.EncodeToString([]byte(raw)), encoded)

			decoded, err := base64.StdEncoding.DecodeString(encoded)
			require.NoError(t, err, "the API server decodes this with the standard alphabet")
			assert.Equal(t, raw, string(decoded))
		})
	}
}

// TestBase64Of_KeepsASecretSecret is the property the doc comment claims, and
// the one that matters: an encoded token that lost its secretness prints in
// full in `pulumi preview`, in the diff of the Secret it is going into.
func TestBase64Of_KeepsASecretSecret(t *testing.T) {
	t.Parallel()

	require.NoError(t, pulumi.RunErr(func(ctx *pulumi.Context) error {
		secret := pulumi.ToSecret(pulumi.String("hcloud-token-value")).(pulumi.StringOutput)

		result, err := internals.UnsafeAwaitOutput(ctx.Context(), layer.Base64Of(secret))
		require.NoError(t, err)

		assert.True(t, result.Secret, "Base64Of dropped the secretness of what it encoded")
		assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("hcloud-token-value")), result.Value)

		return nil
	}, pulumi.WithMocks("layer", "test", noResources{})))
}

func resolve(t *testing.T, value pulumi.StringOutput) string {
	t.Helper()

	result, err := internals.UnsafeAwaitOutput(t.Context(), value)
	require.NoError(t, err)

	encoded, ok := result.Value.(string)
	require.True(t, ok, "expected a string, got %T", result.Value)

	return encoded
}

// noResources is a mock that registers nothing: this test creates no resource,
// it only needs a context in which an output can be marked secret.
type noResources struct{}

func (noResources) NewResource(pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	return "", resource.PropertyMap{}, nil
}

func (noResources) Call(pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}
