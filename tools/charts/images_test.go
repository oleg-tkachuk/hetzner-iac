package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderedImages_ReadsContainersAndInitContainersOnce(t *testing.T) {
	t.Parallel()

	manifests := []byte(`---
apiVersion: apps/v1
kind: DaemonSet
metadata: {name: cilium}
spec:
  template:
    spec:
      initContainers: [{name: config, image: "quay.io/cilium/cilium:v1"}]
      containers: [{name: agent, image: "quay.io/cilium/cilium:v1"}]
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: operator}
spec:
  template:
    spec:
      containers: [{name: operator, image: "quay.io/cilium/operator-generic:v1"}]
---
apiVersion: v1
kind: Pod
metadata: {name: not-a-workload}
spec:
  containers: [{name: x, image: "docker.io/library/busybox:1"}]
`)

	images, err := renderedImages(manifests)
	require.NoError(t, err)
	assert.Equal(t, []string{"quay.io/cilium/cilium:v1", "quay.io/cilium/operator-generic:v1"}, images,
		"an init container's image counts, a repeat does not, and only the workload kinds are read")
}
