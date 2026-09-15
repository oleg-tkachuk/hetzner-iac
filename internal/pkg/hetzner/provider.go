package hetzner

import (
	"fmt"

	"github.com/pulumi/pulumi-hcloud/sdk/go/hcloud"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// ProviderName is the resource name every layer's hcloud provider carries.
//
// One spelling, because it is part of a resource's URN: changing it replaces
// the provider, and a replaced provider replaces everything created with it.
const ProviderName = "hcloud"

// NewProvider builds an hcloud provider from the token the cluster tier
// publishes.
//
// It exists because a layer creating Hetzner resources must NOT use the
// options the layer runner hands it: those carry the Kubernetes provider, and
// a Hetzner resource created with them is created against nothing. Both layers
// that need one had written the same six lines and the same comment.
//
// Lives here rather than on the runner so that internal/pkg/layer stays free of
// pulumi-hcloud — otherwise every Kubernetes-only layer would link the Hetzner
// SDK to use a runner method it never calls.
func NewProvider(ctx *pulumi.Context, token pulumi.StringInput) (*hcloud.Provider, error) {
	provider, err := hcloud.NewProvider(ctx, ProviderName, &hcloud.ProviderArgs{
		Token: token,
	})
	if err != nil {
		return nil, fmt.Errorf("hcloud provider: %w", err)
	}

	return provider, nil
}
