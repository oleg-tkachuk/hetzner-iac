package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ccmDeployment is the shape hcloud-ccm 1.37.0 rendered with HCLOUD_NETWORK
// under `env`: the chart appends its own from networking.network, so the
// container carried the variable twice.
const ccmDeployment = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: hcloud-cloud-controller-manager
spec:
  template:
    spec:
      initContainers:
        - name: wait
          env:
            - name: A
            - name: B
      containers:
        - name: hcloud-cloud-controller-manager
          env:
            - name: HCLOUD_NETWORK
              valueFrom: {secretKeyRef: {name: platform-secret, key: network}}
            - name: HCLOUD_TOKEN
            - name: HCLOUD_NETWORK
              valueFrom: {secretKeyRef: {name: hcloud, key: network}}
`

func TestDuplicateEnv(t *testing.T) {
	t.Parallel()

	found, err := duplicateEnv([]byte(ccmDeployment))
	require.NoError(t, err)

	assert.Equal(t, []string{
		"Deployment/hcloud-cloud-controller-manager container hcloud-cloud-controller-manager " +
			"sets HCLOUD_NETWORK 2 times",
	}, found)
}

func TestDuplicateEnv_DistinctNamesAndOtherKindsPass(t *testing.T) {
	t.Parallel()

	found, err := duplicateEnv([]byte(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: env
data:
  HCLOUD_NETWORK: "1"
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: agent
spec:
  template:
    spec:
      containers:
        - name: a
          env:
            - name: ONE
        - name: b
          env:
            - name: ONE
`))

	require.NoError(t, err)
	assert.Empty(t, found, "the same name in two containers is two variables, not one set twice")
}

func TestDuplicateEnv_AnUnreadableStreamIsAnError(t *testing.T) {
	t.Parallel()

	_, err := duplicateEnv([]byte("kind: Deployment\nspec: [unterminated\n"))

	require.Error(t, err)
}
