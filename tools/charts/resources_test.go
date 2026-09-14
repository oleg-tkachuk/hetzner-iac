package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// workload builds a rendered Deployment carrying one container's resources
// block verbatim, which is the only part of a manifest this gate reads.
func workload(resources string) string {
	return `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: hcloud-csi-controller
  namespace: kube-system
spec:
  template:
    spec:
      containers:
        - name: hcloud-csi-driver
` + resources
}

const bounded = `          resources:
            requests:
              cpu: 10m
              memory: 32Mi
            limits:
              memory: 128Mi
`

func TestUnboundedContainers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		manifests string
		want      []string
	}{
		{
			name:      "a measured container is what the policy asks for",
			manifests: workload(bounded),
		},
		{
			// The state every chart here was in: helm's own defaults set no
			// resources at all, and nothing said so.
			name:      "no resources block at all",
			manifests: workload(""),
			want: []string{
				"Deployment/hcloud-csi-controller container hcloud-csi-driver has no memory request",
				"Deployment/hcloud-csi-controller container hcloud-csi-driver has no memory limit",
			},
		},
		{
			// A request bounds scheduling and nothing else: this container can
			// still grow to the whole node, which also runs etcd.
			name: "a request without a limit",
			manifests: workload(`          resources:
            requests:
              memory: 32Mi
`),
			want: []string{
				"Deployment/hcloud-csi-controller container hcloud-csi-driver has no memory limit",
			},
		},
		{
			name: "a cpu limit is refused, not merely unrequired",
			manifests: workload(`          resources:
            requests:
              cpu: 10m
              memory: 32Mi
            limits:
              cpu: 100m
              memory: 128Mi
`),
			want: []string{
				"Deployment/hcloud-csi-controller container hcloud-csi-driver has a cpu limit, " +
					"which this platform does not use",
			},
		},
		{
			// The case a substring check cannot reach, and the reason this is
			// structural: a chart upgrade ADDS a sidecar, it arrives with no
			// resources, and the containers beside it still have theirs.
			name: "a second container is judged on its own",
			manifests: workload(bounded) + `        - name: csi-attacher
`,
			want: []string{
				"Deployment/hcloud-csi-controller container csi-attacher has no memory request",
				"Deployment/hcloud-csi-controller container csi-attacher has no memory limit",
			},
		},
		{
			// Helm emits comments, empty documents and kinds with no pod
			// template. None of those is a finding.
			name:      "a kind with no pod template",
			manifests: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cilium-config\n",
		},
		{
			name:      "nothing rendered",
			manifests: "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := unboundedContainers([]byte(test.manifests))

			if len(test.want) == 0 {
				assert.Empty(t, got)

				return
			}

			assert.ElementsMatch(t, test.want, got)
		})
	}
}

// TestUnmeasuredCharts_AreNotDeployed keeps the skip list honest.
//
// A skip is a chart nobody is running, and the moment one is deployed the
// numbers can be measured and the line deleted. A chart this platform installs
// and also skips would be exempt from the gate for ever, with the skip line
// reading as if that were deliberate.
func TestUnmeasuredCharts_AreNotDeployed(t *testing.T) {
	t.Parallel()

	require.NotEmpty(t, unmeasuredCharts)

	for key, reason := range unmeasuredCharts {
		assert.Equal(t, reasonUnmeasured, reason,
			"%s is skipped for a different reason than the others; if it is deployed, measure it", key)
	}
}
