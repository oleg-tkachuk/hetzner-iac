package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/imagepolicy"
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

func testInventory(t *testing.T) *imagepolicy.Inventory {
	t.Helper()

	inventory, err := imagepolicy.Parse([]byte(`images:
  - repository: docker.io/library/traefik
    unsigned: {reason: r, tag: v3.7.13, digest: "sha256:` + strings.Repeat("a", 64) + `"}
  - repository: quay.io/cilium/cilium
    signed: {issuer: i, subject: s}
`))
	require.NoError(t, err)

	return inventory
}

func TestPinProblem_DemandsThePinnedDigest(t *testing.T) {
	t.Parallel()

	inventory := testInventory(t)
	traefik, _, err := inventory.Lookup("traefik")
	require.NoError(t, err)

	pinned := "docker.io/traefik:v3.7.13@sha256:" + strings.Repeat("a", 64)
	assert.Empty(t, pinProblem(pinned, traefik))
	assert.Empty(t, pinProblem("docker.io/traefik@sha256:"+strings.Repeat("a", 64), traefik),
		"a bare digest is as pinned as tag@digest")

	assert.Contains(t, pinProblem("docker.io/traefik:v3.7.13", traefik), "is not pinned",
		"a tag alone pulls whatever the tag points to now")
	assert.Contains(t, pinProblem("docker.io/traefik:v3.7.13@sha256:"+strings.Repeat("b", 64), traefik), "is not pinned",
		"a digest other than the inventory's")

	cilium, _, err := inventory.Lookup("quay.io/cilium/cilium")
	require.NoError(t, err)
	assert.Empty(t, pinProblem("quay.io/cilium/cilium:v1", cilium), "a signed image needs no pin")
}

// The case the check exists for: Renovate bumps the chart, its default tag
// moves, and the pin still names the old one.
func TestStalePins_ReportsAChartThatMovedPastItsPin(t *testing.T) {
	t.Parallel()

	inventory := testInventory(t)

	problems, checked := stalePins([]string{"docker.io/traefik:v3.7.13", "quay.io/cilium/cilium:v1"}, inventory)
	assert.Empty(t, problems)
	assert.Equal(t, map[string]bool{"docker.io/library/traefik": true}, checked,
		"only unsigned repositories have a pin to check")

	problems, _ = stalePins([]string{"docker.io/traefik:v3.8.0"}, inventory)
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0], `tag "v3.8.0"`)
	assert.Contains(t, problems[0], "crane digest docker.io/library/traefik:v3.8.0")
}

func TestUncheckedPins_NamesAPinNoDefaultRenderShowed(t *testing.T) {
	t.Parallel()

	inventory := testInventory(t)

	assert.Empty(t, uncheckedPins(inventory, map[string]bool{"docker.io/library/traefik": true}))

	problems := uncheckedPins(inventory, map[string]bool{})
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0], "docker.io/library/traefik")
}

func TestTagOf(t *testing.T) {
	t.Parallel()

	for image, want := range map[string]string{
		"traefik:v3.7.13": "v3.7.13",
		"quay.io/cilium/hubble-ui:v0.13.6@sha256:" + strings.Repeat("a", 64): "v0.13.6",
		"quay.io/cilium/hubble-ui@sha256:" + strings.Repeat("a", 64):         "",
		"traefik":    "",
		"Not A Name": "",
	} {
		assert.Equal(t, want, tagOf(image), image)
	}
}

func TestImageProblems_MarksWhatItFindsAndReportsTheRest(t *testing.T) {
	t.Parallel()

	inventory := testInventory(t)
	used := map[string]bool{}

	problems := imageProblems([]string{
		"quay.io/cilium/cilium:v1",
		"docker.io/library/busybox:1",
		"docker.io/traefik:v3.7.13",
	}, inventory, used)

	require.Len(t, problems, 2)
	assert.Contains(t, problems[0], "busybox:1 has no entry")
	assert.Contains(t, problems[1], "traefik:v3.7.13 is not pinned")
	assert.Equal(t, map[string]bool{"quay.io/cilium/cilium": true, "docker.io/library/traefik": true}, used)
}
