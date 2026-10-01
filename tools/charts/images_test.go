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
kind: ConfigMap
metadata: {name: not-a-pod}
data: {image: "docker.io/library/busybox:1"}
`)

	images, err := renderedImages(manifests)
	require.NoError(t, err)
	assert.Equal(t, []string{"quay.io/cilium/cilium:v1", "quay.io/cilium/operator-generic:v1"}, images,
		"an init container's image counts, a repeat does not, and a kind that runs no pod is skipped")
}

// Admission sees a hook Job's pod like any other, so an image only a Job runs
// needs an entry as much as a Deployment's does.
func TestRenderedImages_ReadsEveryKindThatRunsAPod(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		manifest string
		want     string
	}{
		{
			name: "statefulset",
			manifest: `
apiVersion: apps/v1
kind: StatefulSet
metadata: {name: s}
spec: {template: {spec: {containers: [{name: c, image: "example.org/statefulset:1"}]}}}`,
			want: "example.org/statefulset:1",
		},
		{
			name: "hook job",
			manifest: `
apiVersion: batch/v1
kind: Job
metadata:
  name: startupapicheck
  annotations: {helm.sh/hook: post-install}
spec: {template: {spec: {containers: [{name: c, image: "example.org/job:1"}]}}}`,
			want: "example.org/job:1",
		},
		{
			name: "cronjob",
			manifest: `
apiVersion: batch/v1
kind: CronJob
metadata: {name: cj}
spec:
  schedule: "@daily"
  jobTemplate: {spec: {template: {spec: {containers: [{name: c, image: "example.org/cronjob:1"}]}}}}`,
			want: "example.org/cronjob:1",
		},
		{
			name: "bare pod",
			manifest: `
apiVersion: v1
kind: Pod
metadata: {name: p}
spec: {containers: [{name: c, image: "example.org/pod:1"}]}`,
			want: "example.org/pod:1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			images, err := renderedImages([]byte(tt.manifest))
			require.NoError(t, err)
			assert.Equal(t, []string{tt.want}, images)
		})
	}
}

func TestRenderedImages_RejectsAPodSpecItCannotRead(t *testing.T) {
	t.Parallel()

	_, err := renderedImages([]byte(`
apiVersion: batch/v1
kind: Job
metadata: {name: broken}
spec: {template: {spec: {containers: "not a list"}}}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Job/broken")
}
