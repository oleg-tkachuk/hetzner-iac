package main

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/layer/layertest"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/chartsettings"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterref"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/hetzner"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/layer"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/values"

	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/internals"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// These assertions pin the settings that couple this layer to decisions made
// in the cluster tier. None of them fails an apply when wrong — each produces
// a cluster that comes up and then misbehaves, which is the expensive kind of
// mistake to find.

// ciliumValues is the values YAML this layer would hand Helm, parsed.
//
// Through the template rather than around it: the rendered file is what
// reaches the chart, so a test reading a Go map would check something Helm
// never sees.
func ciliumValues(t *testing.T, controlPlaneCount int) map[string]any {
	t.Helper()

	return ciliumValuesFor(t, controlPlaneCount, hetzner.RoutingModeNative)
}

// ciliumValuesFor renders with an explicit routing mode, because the mode is
// now a topology choice and both halves of it reach Helm from here.
func ciliumValuesFor(t *testing.T, controlPlaneCount int, routingMode string) map[string]any {
	t.Helper()

	return render(t, "cilium", CiliumData(
		pulumi.String(testPodCIDR),
		pulumi.Int(controlPlaneCount),
		pulumi.String(routingMode)))
}

func ccmValues(t *testing.T) map[string]any {
	t.Helper()

	return render(t, "hcloud-ccm", CCMData(pulumi.String(testPodCIDR)))
}

func render(t *testing.T, chart string, data pulumi.Output) map[string]any {
	t.Helper()

	resolvedData, err := internals.UnsafeAwaitOutput(t.Context(), data)
	require.NoError(t, err)

	text, err := values.Render(chart, resolvedData.Value)
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(text), &out), "%s must render valid yaml", chart)

	return out
}

func nestedMap(t *testing.T, in map[string]any, keys ...string) map[string]any {
	t.Helper()

	for _, key := range keys {
		next, ok := in[key].(map[string]any)
		require.True(t, ok, "no map at %q", key)

		in = next
	}

	return in
}

// testPodCIDR is what the cluster tier publishes as podCidr.
const testPodCIDR = "10.244.0.0/16"

func TestCiliumValues_ReplacesKubeProxy(t *testing.T) {
	t.Parallel()

	// Talos was configured with kube-proxy disabled. Without the replacement
	// the cluster has no service dataplane and every ClusterIP blackholes —
	// with no error anywhere.
	assert.Equal(t, true, ciliumValues(t, 3)[chartsettings.CiliumKubeProxyReplacement])
}

func TestCiliumValues_TalksToTheAPIThroughKubePrism(t *testing.T) {
	t.Parallel()

	// A node-local load balancer over the control plane: Cilium keeps working
	// while a control-plane node is being replaced. Pointing at a node address
	// instead would tie the CNI to one control-plane node's life.
	rendered := ciliumValues(t, 3)

	assert.Equal(t, KubePrismHost, rendered[chartsettings.CiliumK8sServiceHost])
	assert.Equal(t, float64(chartsettings.KubePrismPort), rendered[chartsettings.CiliumK8sServicePort])
	assert.Equal(t, 7445, chartsettings.KubePrismPort,
		"KubePrism port must match the machine config written by the cluster tier")
}

func TestCiliumValues_UsesNativeRoutingOverThePodCIDR(t *testing.T) {
	t.Parallel()

	// Native routing depends on the CCM's route controller from this layer and
	// on the gateway route the cluster tier writes into every machine config.
	// The pod CIDR has to be the cluster's actual one: a wrong value here
	// masquerades traffic that should be routed.
	rendered := ciliumValues(t, 3)

	assert.Equal(t, hetzner.RoutingModeNative, rendered["routingMode"])
	assert.Equal(t, testPodCIDR, rendered["ipv4NativeRoutingCIDR"])
}

func TestCiliumValues_CarryTheRoutingModeTheTopologyChose(t *testing.T) {
	t.Parallel()

	// Both modes reach Helm from here, so both are rendered. A mode that
	// silently fell back to the other would be a datapath nobody chose.
	for _, mode := range hetzner.RoutingModes {
		assert.Equal(t, mode, ciliumValuesFor(t, 3, mode)["routingMode"], mode)
	}
}

func TestCiliumValues_NeverAskForDirectNodeRoutes(t *testing.T) {
	t.Parallel()

	// false in BOTH modes, and this is the regression. autoDirectNodeRoutes
	// asks Cilium to route a peer's pod CIDR via that peer's address, and a
	// Hetzner private network gives each server a /32 with only the gateway
	// on-link. Cilium refuses — "must be directly reachable" — so pod-to-pod
	// across nodes had no route at all while it was true. A single-node
	// cluster hid it completely, because nothing crossed a node.
	for _, mode := range hetzner.RoutingModes {
		assert.Equal(t, false, ciliumValuesFor(t, 3, mode)["autoDirectNodeRoutes"],
			"%s: autoDirectNodeRoutes cannot work on a Hetzner private network", mode)
	}
}

func TestCiliumValues_AccommodatesTalosCgroups(t *testing.T) {
	t.Parallel()

	// Talos mounts cgroups itself and runs a read-only root. Letting Cilium
	// automount produces an agent that crash-loops on start.
	cgroup := nestedMap(t, ciliumValues(t, 3), "cgroup")

	assert.Equal(t, false, nestedMap(t, cgroup, "autoMount")["enabled"])
	assert.Equal(t, "/sys/fs/cgroup", cgroup["hostRoot"])
}

func TestCiliumValues_GrantsTheCapabilitiesTalosRequires(t *testing.T) {
	t.Parallel()

	// Under Talos the agent is not fully privileged, so every capability it
	// needs has to be named. A missing one shows up as an agent that starts
	// and then fails to programme eBPF.
	capabilities := nestedMap(t, ciliumValues(t, 3), "securityContext", "capabilities")

	granted, ok := capabilities["ciliumAgent"].([]any)
	require.True(t, ok)

	for _, capability := range []string{
		"NET_ADMIN", "NET_RAW", "SYS_ADMIN", "SYS_RESOURCE", "IPC_LOCK",
	} {
		assert.Contains(t, granted, capability)
	}
}

func TestCiliumValues_CreatesNoServiceMonitors(t *testing.T) {
	t.Parallel()

	// Nothing in this repository installs the Prometheus
	// operator CRDs any more — observability is deployed through Argo CD — so
	// a ServiceMonitor here is a resource whose kind does not exist, and the
	// layer would fail on a cluster that has not been given one.
	rendered := ciliumValues(t, 3)

	// Three places, and the nesting differs in each — which is exactly the
	// shape a test should pin, because setting the wrong key silently leaves
	// the monitor enabled.
	for _, path := range [][]string{
		{"prometheus", "serviceMonitor"},
		{"hubble", "metrics", "serviceMonitor"},
		{"operator", "prometheus", "serviceMonitor"},
	} {
		assert.Equal(t, false, nestedMap(t, rendered, path...)["enabled"], path)
	}
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

	// LoadTopology substitutes 1 for a missing count and internal/pkg/clusterref's
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
	// that its result reaches the chart, on the key the chart reads. The count
	// comes from another stack, so what is asserted is the rendered value for
	// a given published count.
	assert.Equal(t, float64(1), nestedMap(t, ciliumValues(t, 1), "operator")["replicas"],
		"one control-plane node, one operator replica")

	assert.Equal(t, float64(OperatorReplicasWanted),
		nestedMap(t, ciliumValues(t, 3), "operator")["replicas"])
}

func TestCCMValues_EnablesTheRouteController(t *testing.T) {
	t.Parallel()

	// Cilium is configured for native routing, which depends on the CCM
	// writing a route per node. With the route controller off, the CCM starts
	// cleanly and manages no routes — and pods cannot reach pods on other
	// nodes, with nothing in either component's logs saying why.
	networking := nestedMap(t, ccmValues(t), "networking")

	assert.Equal(t, true, networking["enabled"])
	assert.Equal(t, testPodCIDR, networking["clusterCIDR"])
}

func TestCCMValues_ReadsBothCredentialsFromTheSharedSecret(t *testing.T) {
	t.Parallel()

	// The network id is as necessary as the token: without it the route
	// controller has no network to write routes into. And the name has to be
	// the Secret this layer creates — a mismatch produces pods that start and
	// then fail to authenticate against the Hetzner API.
	env := nestedMap(t, ccmValues(t), "env")

	for name, key := range map[string]string{
		"HCLOUD_TOKEN":   "token",
		"HCLOUD_NETWORK": "network",
	} {
		ref := nestedMap(t, env, name, "valueFrom", "secretKeyRef")

		assert.Equal(t, CredentialsSecret, ref["name"], name)
		assert.Equal(t, key, ref["key"], name)
	}
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
		resource.PropertyKey(clusterref.OutputNodeSubnet):        resource.NewStringProperty("10.0.1.0/24"),
		resource.PropertyKey(clusterref.OutputPodCIDR):           resource.NewStringProperty("10.244.0.0/16"),
		resource.PropertyKey(clusterref.OutputServiceCIDR):       resource.NewStringProperty("10.96.0.0/12"),
		resource.PropertyKey(clusterref.OutputClusterName):       resource.NewStringProperty("platform-test"),
		resource.PropertyKey(clusterref.OutputLocation):          resource.NewStringProperty(platform.ProbeLocation),
		resource.PropertyKey(clusterref.OutputHcloudToken):       resource.NewStringProperty(m.exported),
		resource.PropertyKey(clusterref.OutputControlPlaneCount): resource.NewNumberProperty(3),
		resource.PropertyKey(clusterref.OutputRoutingMode):       resource.NewStringProperty(hetzner.RoutingModeNative),
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

		_, deployErr := runner.Deploy(Components)

		return deployErr
	}, pulumi.WithMocks(testProject, testStack, m)))

	// The credentials Secret and the CCM name Cilium directly. The CSI driver
	// reaches it through the CCM, which reaches it here.
	for _, name := range []string{CredentialsSecret, "hcloud-cloud-controller-manager"} {
		assert.True(t, m.dependsOnCilium(name),
			"%s must depend on cilium: without it the engine may create it on a NotReady node", name)
	}
}

func TestComponents(t *testing.T) {
	t.Parallel()

	layertest.Check(t, Components)
}
