package main

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/chartsettings"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/clusterref"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/layer"

	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"

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

// ---------------------------------------------------------------------------
// Where the token comes from
// ---------------------------------------------------------------------------

const (
	testProject = "node-platform"
	testStack   = "test"
)

// stackMocks stands in for the cluster tier. exported == "" models a cluster
// built from an environment token rather than from stack config, which the
// tier exports as an empty string.
type stackMocks struct{ exported string }

// outputs is the whole declared set, always: the contract is total, and the
// tier exports an empty token rather than none when it has none.
func (m stackMocks) outputs() resource.PropertyMap {
	return resource.PropertyMap{
		resource.PropertyKey(clusterref.OutputContractVersion):   resource.NewNumberProperty(clusterref.ContractVersion),
		resource.PropertyKey(clusterref.OutputKubeconfig):        resource.NewStringProperty("apiVersion: v1"),
		resource.PropertyKey(clusterref.OutputTalosconfig):       resource.NewStringProperty("context: test"),
		resource.PropertyKey(clusterref.OutputEndpoint):          resource.NewStringProperty("https://203.0.113.200:6443"),
		resource.PropertyKey(clusterref.OutputAPILoadBalancerIP): resource.NewStringProperty(""),
		resource.PropertyKey(clusterref.OutputNetworkID):         resource.NewNumberProperty(12637895),
		resource.PropertyKey(clusterref.OutputPodCIDR):           resource.NewStringProperty("10.244.0.0/16"),
		resource.PropertyKey(clusterref.OutputServiceCIDR):       resource.NewStringProperty("10.96.0.0/12"),
		resource.PropertyKey(clusterref.OutputClusterName):       resource.NewStringProperty("platform-test"),
		resource.PropertyKey(clusterref.OutputLocation):          resource.NewStringProperty("hel1"),
		resource.PropertyKey(clusterref.OutputHcloudToken):       resource.NewStringProperty(m.exported),
		resource.PropertyKey(clusterref.OutputControlPlaneCount): resource.NewNumberProperty(3),
	}
}

func (m stackMocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	if args.TypeToken != "pulumi:pulumi:StackReference" {
		return args.Name, args.Inputs, nil
	}

	return args.Name, resource.PropertyMap{"outputs": resource.NewObjectProperty(m.outputs())}, nil
}

func (stackMocks) Call(pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

// resolved runs resolveToken against a cluster stack exporting `exported` and
// a layer config holding `override`, and returns the token the layer would put
// into the credentials Secret.
//
// Config reaches a Pulumi program through PULUMI_CONFIG, and t.Setenv forbids
// t.Parallel, so these tests run in sequence.
func resolved(t *testing.T, exported, override string) (string, error) {
	t.Helper()

	// layer.New needs the stack reference, the same as the program does.
	cfg := map[string]string{testProject + ":clusterStackRef": "acme/hetzner-cluster/test"}
	if override != "" {
		cfg[testProject+":hcloudToken"] = override
	}

	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	t.Setenv("PULUMI_CONFIG", string(raw))

	var (
		got  string
		done = make(chan struct{})
	)

	err = pulumi.RunErr(func(ctx *pulumi.Context) error {
		// Through layer.New rather than assembling the three fields by hand:
		// resolveToken takes the runner, so the test exercises the same
		// wiring the program does.
		runner, newErr := layer.New(ctx)
		if newErr != nil {
			return newErr
		}

		token := resolveToken(runner)

		// The token has to reach a resource. An output nothing consumes is
		// never awaited, so an error raised inside it hangs the program
		// instead of failing it — which is how this test first behaved.
		// main() puts the token in exactly this Secret.
		if _, secretErr := corev1.NewSecret(ctx, CredentialsSecret, &corev1.SecretArgs{
			StringData: pulumi.StringMap{"token": token},
		}); secretErr != nil {
			return secretErr
		}

		token.ApplyT(func(value string) string {
			got = value

			close(done)

			return value
		})

		return nil
	}, pulumi.WithMocks(testProject, testStack, stackMocks{exported: exported}))
	if err != nil {
		// The apply never ran, so `done` stays open — returning here rather
		// than waiting is what keeps a failing case from hanging the suite.
		return "", err
	}

	<-done

	return got, nil
}

func TestResolveToken_TakesTheClusterTiersToken(t *testing.T) {
	// The point of the whole arrangement: the token is set once, in the stack
	// whose provider already holds it, and this layer keeps no copy.
	got, err := resolved(t, "token-from-the-cluster-tier", "")

	require.NoError(t, err)
	assert.Equal(t, "token-from-the-cluster-tier", got)
}

func TestResolveToken_LayerConfigOverridesTheClusterTier(t *testing.T) {
	// Kept deliberately: an operator may want the CCM and CSI to authenticate
	// with a token scoped differently from the one that built the cluster.
	got, err := resolved(t, "token-from-the-cluster-tier", "token-from-this-layer")

	require.NoError(t, err)
	assert.Equal(t, "token-from-this-layer", got)
}

func TestResolveToken_NoTokenAnywhereFailsWithTheRemedy(t *testing.T) {
	// Without this the layer creates a Secret holding an empty token, and the
	// CCM starts, logs 401 and never clears the uninitialized taint — which
	// reads as a broken cluster rather than as a missing credential.
	_, err := resolved(t, "", "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), clusterref.OutputHcloudToken)
	assert.Contains(t, err.Error(), "config set --secret hcloud:token")
	assert.Contains(t, err.Error(), "config set --secret node-platform:hcloudToken")
}

// ---------------------------------------------------------------------------
// The ordering this stack exists to guarantee
// ---------------------------------------------------------------------------

// dependencyMocks records the dependency edges the engine was given, from the
// register RPC rather than from the order the mock happened to be called in.
//
// That distinction cost a rewrite: the first version of this test recorded
// call order, passed, and kept passing with the DependsOn deleted — because
// with an instant mock the calls arrive in program order whether or not
// anything depends on anything.
type dependencyMocks struct {
	mu   sync.Mutex
	deps map[string][]string
}

func newDependencyMocks() *dependencyMocks {
	return &dependencyMocks{deps: map[string][]string{}}
}

func (m *dependencyMocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	if args.TypeToken == "pulumi:pulumi:StackReference" {
		return args.Name, resource.PropertyMap{
			"outputs": resource.NewObjectProperty(stackMocks{exported: "token"}.outputs()),
		}, nil
	}

	if args.RegisterRPC != nil {
		m.mu.Lock()
		m.deps[args.Name] = append(m.deps[args.Name], args.RegisterRPC.GetDependencies()...)
		m.mu.Unlock()
	}

	return args.Name, args.Inputs, nil
}

func (*dependencyMocks) Call(pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

// dependsOnCilium reports whether any recorded dependency of name is Cilium.
func (m *dependencyMocks) dependsOnCilium(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, urn := range m.deps[name] {
		if strings.Contains(urn, "::cilium") {
			return true
		}
	}

	return false
}

func TestProgram_NothingIsInstalledBeforeCilium(t *testing.T) {
	// The one thing merging two stacks into one was for. The CCM chart renders
	// a Deployment whose tolerations cover node.kubernetes.io/not-ready only
	// with effect NoExecute, so it cannot be scheduled until a CNI has made
	// the node Ready. As two stacks that ordering lived in directory names and
	// in the order a taskfile walked them — nothing stopped anyone applying
	// the second alone, and doing so sat Pending for a ten-minute timeout
	// before Helm rolled it back. Here the engine refuses to.
	raw, err := json.Marshal(map[string]string{
		testProject + ":clusterStackRef": "acme/hetzner-cluster/test",
	})
	require.NoError(t, err)
	t.Setenv("PULUMI_CONFIG", string(raw))

	m := newDependencyMocks()

	require.NoError(t, pulumi.RunErr(func(ctx *pulumi.Context) error {
		runner, newErr := layer.New(ctx)
		if newErr != nil {
			return newErr
		}

		return program(runner)
	}, pulumi.WithMocks(testProject, testStack, m)))

	// The credentials Secret and the CCM name Cilium directly. The CSI driver
	// reaches it through the CCM, which reaches it here.
	for _, name := range []string{CredentialsSecret, "hcloud-cloud-controller-manager"} {
		assert.True(t, m.dependsOnCilium(name),
			"%s must depend on cilium: without it the engine may create it on a NotReady node", name)
	}
}
