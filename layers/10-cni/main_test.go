package main

import (
	"testing"
	"time"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/chartsettings"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These assertions pin the settings that couple this layer to decisions made
// in the cluster tier. None of them fails an apply when wrong — each produces
// a cluster that comes up and then misbehaves, which is the expensive kind of
// mistake to find.

func TestCiliumValues_ReplacesKubeProxy(t *testing.T) {
	t.Parallel()

	// Talos was configured with kube-proxy disabled. Without the replacement
	// the cluster has no service dataplane and every ClusterIP blackholes —
	// with no error anywhere.
	values := CiliumValues(pulumi.String("10.244.0.0/16"), pulumi.Int(3))

	assert.Equal(t, pulumi.Bool(true), values[chartsettings.CiliumKubeProxyReplacement])
}

func TestCiliumValues_TalksToTheAPIThroughKubePrism(t *testing.T) {
	t.Parallel()

	// A node-local load balancer over the control plane: Cilium keeps working
	// while a control-plane node is being replaced. Pointing at a node address
	// instead would tie the CNI to one control-plane node's life.
	values := CiliumValues(pulumi.String("10.244.0.0/16"), pulumi.Int(3))

	assert.Equal(t, pulumi.String("localhost"), values[chartsettings.CiliumK8sServiceHost])
	assert.Equal(t, pulumi.Int(chartsettings.KubePrismPort), values[chartsettings.CiliumK8sServicePort])
	assert.Equal(t, 7445, chartsettings.KubePrismPort,
		"KubePrism port must match the machine config written by the cluster tier")
}

func TestCiliumValues_UsesNativeRoutingOverThePodCIDR(t *testing.T) {
	t.Parallel()

	// Native routing depends on the CCM's route controller from
	// 10-cloud-integration. The pod CIDR has to be the cluster's actual one:
	// a wrong value here masquerades traffic that should be routed.
	podCIDR := pulumi.String("10.244.0.0/16")
	values := CiliumValues(podCIDR, pulumi.Int(3))

	assert.Equal(t, pulumi.String("native"), values["routingMode"])
	assert.Equal(t, podCIDR, values["ipv4NativeRoutingCIDR"])
	assert.Equal(t, pulumi.Bool(true), values["autoDirectNodeRoutes"])
}

func TestCiliumValues_AccommodatesTalosCgroups(t *testing.T) {
	t.Parallel()

	// Talos mounts cgroups itself and runs a read-only root. Letting Cilium
	// automount produces an agent that crash-loops on start.
	values := CiliumValues(pulumi.String("10.244.0.0/16"), pulumi.Int(3))

	cgroup, ok := values["cgroup"].(pulumi.Map)
	require.True(t, ok)

	autoMount, ok := cgroup["autoMount"].(pulumi.Map)
	require.True(t, ok)

	assert.Equal(t, pulumi.Bool(false), autoMount["enabled"])
	assert.Equal(t, pulumi.String("/sys/fs/cgroup"), cgroup["hostRoot"])
}

func TestCiliumValues_GrantsTheCapabilitiesTalosRequires(t *testing.T) {
	t.Parallel()

	// Under Talos the agent is not fully privileged, so every capability it
	// needs has to be named. A missing one shows up as an agent that starts
	// and then fails to programme eBPF.
	values := CiliumValues(pulumi.String("10.244.0.0/16"), pulumi.Int(3))

	securityContext, ok := values["securityContext"].(pulumi.Map)
	require.True(t, ok)

	capabilities, ok := securityContext["capabilities"].(pulumi.Map)
	require.True(t, ok)

	agent, ok := capabilities["ciliumAgent"].(pulumi.StringArrayInput)
	require.True(t, ok)

	granted := resolveStrings(t, agent)
	for _, capability := range []string{
		"NET_ADMIN", "NET_RAW", "SYS_ADMIN", "SYS_RESOURCE", "IPC_LOCK",
	} {
		assert.Contains(t, granted, capability)
	}
}

func TestCiliumValues_CreatesNoServiceMonitors(t *testing.T) {
	t.Parallel()

	// The Prometheus operator CRDs belong to 60-observability. A
	// ServiceMonitor here would make this layer fail on a cluster where that
	// layer is not installed, which would break the independence the whole
	// layout is for.
	values := CiliumValues(pulumi.String("10.244.0.0/16"), pulumi.Int(3))

	prometheus, ok := values["prometheus"].(pulumi.Map)
	require.True(t, ok)

	prometheusMonitor, ok := prometheus["serviceMonitor"].(pulumi.Map)
	require.True(t, ok)
	assert.Equal(t, pulumi.Bool(false), prometheusMonitor["enabled"])

	// Hubble nests its monitor under metrics rather than at the top level —
	// which is exactly the kind of shape a test should pin, because setting
	// the wrong key silently leaves the monitor enabled.
	hubble, ok := values["hubble"].(pulumi.Map)
	require.True(t, ok)

	hubbleMetrics, ok := hubble["metrics"].(pulumi.Map)
	require.True(t, ok)

	hubbleMonitor, ok := hubbleMetrics["serviceMonitor"].(pulumi.Map)
	require.True(t, ok)
	assert.Equal(t, pulumi.Bool(false), hubbleMonitor["enabled"])

	operator, ok := values["operator"].(pulumi.Map)
	require.True(t, ok)

	operatorPrometheus, ok := operator["prometheus"].(pulumi.Map)
	require.True(t, ok)

	monitor, ok := operatorPrometheus["serviceMonitor"].(pulumi.Map)
	require.True(t, ok)
	assert.Equal(t, pulumi.Bool(false), monitor["enabled"])
}

// resolveStrings pulls the plain values out of a StringArrayInput built from
// literals, which is all these values ever are.
func resolveStrings(t *testing.T, input pulumi.StringArrayInput) []string {
	t.Helper()

	array, ok := input.(pulumi.StringArray)
	require.True(t, ok, "expected a literal StringArray")

	out := make([]string, 0, len(array))

	for _, item := range array {
		value, ok := item.(pulumi.String)
		require.True(t, ok)

		out = append(out, string(value))
	}

	return out
}

func TestOperatorReplicas_CapsAtOnePerControlPlaneNode(t *testing.T) {
	t.Parallel()

	// Each replica binds a host port, so the count cannot exceed the nodes
	// there are to put them on.
	for name, tc := range map[string]struct {
		count int
		want  int
	}{
		"single node":     {1, 1},
		"two":             {2, 2},
		"three":           {3, OperatorReplicasWanted},
		"more than wants": {9, OperatorReplicasWanted},
	} {
		assert.Equal(t, tc.want, operatorReplicas(tc.count), name)
	}
}
func TestOperatorReplicas_ACountThatCannotBeRealKeepsTheDefault(t *testing.T) {
	t.Parallel()

	// LoadTopology substitutes 1 for a missing count and pkg/clusterref's
	// version gate rules out an absent one, so zero can only mean a broken
	// producer. Sizing against it would scale a working operator to nothing,
	// so the wanted count stands and the extra replica is the visible symptom.
	for _, count := range []int{0, -1} {
		assert.Equal(t, OperatorReplicasWanted, operatorReplicas(count), count)
	}
}

func TestCiliumValues_SizesTheOperatorFromTheClusterCount(t *testing.T) {
	t.Parallel()

	// operatorReplicas is tested above on plain numbers; this is the wiring —
	// that its result is what reaches the chart, on the key the chart reads.
	operator, ok := CiliumValues(pulumi.String("10.244.0.0/16"), pulumi.Int(1))["operator"].(pulumi.Map)
	require.True(t, ok)

	replicas, ok := operator["replicas"].(pulumi.IntOutput)
	require.True(t, ok, "replicas must stay an output: the count comes from another stack")

	got := make(chan int, 1)

	replicas.ApplyT(func(value int) int {
		got <- value

		return value
	})

	select {
	case value := <-got:
		assert.Equal(t, 1, value, "one control-plane node, one operator replica")
	case <-time.After(10 * time.Second):
		t.Fatal("replicas never resolved")
	}
}
