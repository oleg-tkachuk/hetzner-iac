package main

import (
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCCMValues_EnablesTheRouteController(t *testing.T) {
	t.Parallel()

	// Cilium is configured for native routing, which depends on the CCM
	// writing a route per node. With the route controller off, the CCM starts
	// cleanly and manages no routes — and pods cannot reach pods on other
	// nodes, with nothing in either component's logs saying why.
	podCIDR := pulumi.String("10.244.0.0/16")

	networking, ok := CCMValues(podCIDR)["networking"].(pulumi.Map)
	require.True(t, ok)

	assert.Equal(t, pulumi.Bool(true), networking["enabled"])
	assert.Equal(t, podCIDR, networking["clusterCIDR"])
}

func TestCCMValues_ReadsBothCredentialKeys(t *testing.T) {
	t.Parallel()

	// The network id is as necessary as the token: without it the route
	// controller has no network to write routes into.
	env, ok := CCMValues(pulumi.String("10.244.0.0/16"))["env"].(pulumi.Map)
	require.True(t, ok)

	for name, key := range map[string]string{
		"HCLOUD_TOKEN":   "token",
		"HCLOUD_NETWORK": "network",
	} {
		entry, ok := env[name].(pulumi.Map)
		require.True(t, ok, name)

		valueFrom, ok := entry["valueFrom"].(pulumi.Map)
		require.True(t, ok, name)

		ref, ok := valueFrom["secretKeyRef"].(pulumi.Map)
		require.True(t, ok, name)

		assert.Equal(t, pulumi.String(CredentialsSecret), ref["name"], name)
		assert.Equal(t, pulumi.String(key), ref["key"], name)
	}
}

func TestSecretRef_PointsAtTheSharedSecret(t *testing.T) {
	t.Parallel()

	// Both charts default to a secret with this name; a mismatch produces
	// pods that start and then fail to authenticate against the Hetzner API.
	ref := SecretRef("token")

	valueFrom, ok := ref["valueFrom"].(pulumi.Map)
	require.True(t, ok)

	secretKeyRef, ok := valueFrom["secretKeyRef"].(pulumi.Map)
	require.True(t, ok)

	assert.Equal(t, pulumi.String("hcloud"), secretKeyRef["name"])
	assert.Equal(t, pulumi.String("token"), secretKeyRef["key"])
}
