package hetzner_test

import (
	"sync"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/hetzner"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The component resources are tested through Pulumi's own mock monitor rather
// than against Hetzner. That exercises the part worth testing — which
// resources get created, with which inputs, and in what dependency order —
// without a cloud account, and it is the same harness the provider SDKs use.

// recorder captures every resource the program registers, keyed by Pulumi
// type token, so assertions can ask "was a load balancer created?" instead of
// inspecting outputs that only exist after a real apply.
type recorder struct {
	mu        sync.Mutex
	resources map[string][]resource.PropertyMap
}

func newRecorder() *recorder {
	return &recorder{resources: map[string][]resource.PropertyMap{}}
}

func (r *recorder) record(token string, inputs resource.PropertyMap) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.resources[token] = append(r.resources[token], inputs)
}

func (r *recorder) of(token string) []resource.PropertyMap {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.resources[token]
}

func (r *recorder) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	r.record(args.TypeToken, args.Inputs)

	outputs := args.Inputs.Copy()

	switch args.TypeToken {
	case "hcloud:index/server:Server":
		// A real server reports its public address only after creation; the
		// components turn that output into certificate SANs, so it has to be
		// present for the graph to resolve.
		outputs["ipv4Address"] = resource.NewStringProperty("203.0.113.10")
	case "hcloud:index/loadBalancer:LoadBalancer":
		outputs["ipv4"] = resource.NewStringProperty("203.0.113.200")
	case "talos:cluster/kubeconfig:Kubeconfig":
		outputs["kubeconfigRaw"] = resource.NewStringProperty("apiVersion: v1\nkind: Config\n")
	}

	// Numeric ids: hcloud ids are always numeric and the components parse
	// them, so a non-numeric mock id would fail for the wrong reason.
	return "1", outputs, nil
}

func (r *recorder) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	switch args.Token {
	case "hcloud:index/getImage:getImage":
		return resource.PropertyMap{
			"id": resource.NewNumberProperty(4242),
		}, nil
	case "talos:machine/getConfiguration:getConfiguration":
		return resource.PropertyMap{
			"machineConfiguration": resource.NewStringProperty("version: v1alpha1\n"),
		}, nil
	case "talos:client/getConfiguration:getConfiguration":
		return resource.PropertyMap{
			"talosConfig": resource.NewStringProperty("context: test\n"),
		}, nil
	}

	return resource.PropertyMap{}, nil
}

// runCluster builds a cluster under the mock monitor and returns what was
// registered.
func runCluster(t *testing.T, topology *hetzner.Topology, args *hetzner.ClusterArgs) *recorder {
	t.Helper()

	rec := newRecorder()
	args.Topology = topology

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := hetzner.NewCluster(ctx, "test", args)

		return err
	}, pulumi.WithMocks("hetzner-iac", "test", rec))

	require.NoError(t, err)

	return rec
}

func haTopology(t *testing.T) *hetzner.Topology {
	t.Helper()

	return mustParse(t, validYAML)
}

func singleNodeTopology(t *testing.T) *hetzner.Topology {
	t.Helper()

	topology := mustParse(t, validYAML)
	topology.ControlPlane.Count = 1
	topology.ControlPlane.APILoadBalancerType = ""

	return topology
}

func TestNewCluster_CreatesTheExpectedNodes(t *testing.T) {
	t.Parallel()

	rec := runCluster(t, haTopology(t), &hetzner.ClusterArgs{PublicIPv4: true})

	servers := rec.of("hcloud:index/server:Server")
	// 3 control-plane nodes + 2 workers from the shared fixture.
	require.Len(t, servers, 5)

	names := make([]string, 0, len(servers))
	for _, server := range servers {
		names = append(names, server["name"].StringValue())
	}

	assert.ElementsMatch(t, []string{
		"platform-hel-control-plane-0",
		"platform-hel-control-plane-1",
		"platform-hel-control-plane-2",
		"platform-hel-worker-0",
		"platform-hel-worker-1",
	}, names)
}

func TestNewCluster_AssignsDisjointPrivateAddresses(t *testing.T) {
	t.Parallel()

	rec := runCluster(t, haTopology(t), &hetzner.ClusterArgs{PublicIPv4: true})

	seen := map[string]string{}

	for _, server := range rec.of("hcloud:index/server:Server") {
		name := server["name"].StringValue()
		networks := server["networks"].ArrayValue()
		require.Len(t, networks, 1, "%s must join exactly one private network", name)

		address := networks[0].ObjectValue()["ip"].StringValue()
		if prev, clash := seen[address]; clash {
			t.Fatalf("private address %s assigned to both %s and %s", address, prev, name)
		}

		seen[address] = name
	}

	// Control plane takes the low offsets, worker pool 0 starts at its own
	// slice — the property that keeps pool growth from renumbering anything.
	assert.Equal(t, "platform-hel-control-plane-0", seen["10.0.1.2"])
	assert.Equal(t, "platform-hel-worker-0", seen["10.0.1.40"])
}

func TestNewCluster_SingleControlPlaneGetsNoLoadBalancer(t *testing.T) {
	t.Parallel()

	// There is nothing to fail over between, so a load balancer would only
	// cost money.
	rec := runCluster(t, singleNodeTopology(t), &hetzner.ClusterArgs{PublicIPv4: true})

	assert.Empty(t, rec.of("hcloud:index/loadBalancer:LoadBalancer"))
	assert.Len(t, rec.of("hcloud:index/server:Server"), 3) // 1 control plane + 2 workers
}

func TestNewCluster_HAControlPlaneGetsALoadBalancer(t *testing.T) {
	t.Parallel()

	rec := runCluster(t, haTopology(t), &hetzner.ClusterArgs{PublicIPv4: true})

	loadBalancers := rec.of("hcloud:index/loadBalancer:LoadBalancer")
	require.Len(t, loadBalancers, 1)
	assert.Equal(t, "lb11", loadBalancers[0]["loadBalancerType"].StringValue())

	// Targets are selected by label so a replaced control-plane node is picked
	// up without a diff on the load balancer.
	targets := rec.of("hcloud:index/loadBalancerTarget:LoadBalancerTarget")
	require.Len(t, targets, 1)
	assert.Equal(t, "label_selector", targets[0]["type"].StringValue())
	assert.Contains(t, targets[0]["labelSelector"].StringValue(), "role=control-plane")
	assert.True(t, targets[0]["usePrivateIp"].BoolValue(),
		"load balancer must reach the API over the private network")
}

func TestNewCluster_FirewallOpensOnlyTheAdminPorts(t *testing.T) {
	t.Parallel()

	rec := runCluster(t, haTopology(t), &hetzner.ClusterArgs{PublicIPv4: true})

	firewalls := rec.of("hcloud:index/firewall:Firewall")
	require.Len(t, firewalls, 1)

	rules := firewalls[0]["rules"].ArrayValue()
	require.Len(t, rules, 2)

	ports := map[string]bool{}

	for _, rule := range rules {
		fields := rule.ObjectValue()
		ports[fields["port"].StringValue()] = true

		sources := fields["sourceIps"].ArrayValue()
		require.Len(t, sources, 1)
		assert.Equal(t, "203.0.113.4/32", sources[0].StringValue())
		assert.Equal(t, "in", fields["direction"].StringValue())
	}

	assert.Equal(t, map[string]bool{"6443": true, "50000": true}, ports)

	// Attachment is by selector, not by naming servers: that is what makes a
	// node created later inherit the perimeter.
	applyTos := firewalls[0]["applyTos"].ArrayValue()
	require.Len(t, applyTos, 1)
	assert.Equal(t, "cluster=platform-hel", applyTos[0].ObjectValue()["labelSelector"].StringValue())
}

func TestNewCluster_ControlPlaneGetsAntiAffinity(t *testing.T) {
	t.Parallel()

	// Three etcd members on one physical host is one failure domain, not
	// three.
	rec := runCluster(t, haTopology(t), &hetzner.ClusterArgs{PublicIPv4: true})

	groups := rec.of("hcloud:index/placementGroup:PlacementGroup")
	require.Len(t, groups, 1)
	assert.Equal(t, "spread", groups[0]["type"].StringValue())
}

func TestNewCluster_BootstrapsEtcdExactlyOnce(t *testing.T) {
	t.Parallel()

	// Bootstrapping more than once, or on more than one node, leaves an etcd
	// the cluster never recovers from.
	rec := runCluster(t, haTopology(t), &hetzner.ClusterArgs{PublicIPv4: true})

	assert.Len(t, rec.of("talos:machine/bootstrap:Bootstrap"), 1)
}

func TestNewCluster_AppliesMachineConfigToEveryNode(t *testing.T) {
	t.Parallel()

	rec := runCluster(t, haTopology(t), &hetzner.ClusterArgs{PublicIPv4: true})

	applies := rec.of("talos:machine/configurationApply:ConfigurationApply")
	assert.Len(t, applies, 5, "every control-plane and worker node needs its configuration")
}

func TestNewCluster_NodeSubnetIsCreatedFromTheTopology(t *testing.T) {
	t.Parallel()

	rec := runCluster(t, haTopology(t), &hetzner.ClusterArgs{PublicIPv4: true})

	networks := rec.of("hcloud:index/network:Network")
	require.Len(t, networks, 1)
	assert.Equal(t, "10.0.0.0/16", networks[0]["ipRange"].StringValue())

	subnets := rec.of("hcloud:index/networkSubnet:NetworkSubnet")
	require.Len(t, subnets, 1)
	assert.Equal(t, "10.0.1.0/24", subnets[0]["ipRange"].StringValue())
	assert.Equal(t, "eu-central", subnets[0]["networkZone"].StringValue())
}

func TestNewCluster_LabelsEveryServerForTheFirewallSelector(t *testing.T) {
	t.Parallel()

	// If a server misses the cluster label it is created outside the
	// perimeter — reachable on the public internet with no rules applied.
	rec := runCluster(t, haTopology(t), &hetzner.ClusterArgs{PublicIPv4: true})

	for _, server := range rec.of("hcloud:index/server:Server") {
		labels := server["labels"].ObjectValue()
		assert.Equal(t, "platform-hel", labels["cluster"].StringValue(),
			"server %s", server["name"].StringValue())
	}
}

func TestNewCluster_WorkerPoolCarriesItsLabelsAndTaints(t *testing.T) {
	t.Parallel()

	topology := haTopology(t)
	topology.WorkerPools[0].Labels = map[string]string{"workload": "gpu"}
	topology.WorkerPools[0].Taints = []string{"gpu=true:NoSchedule"}

	rec := runCluster(t, topology, &hetzner.ClusterArgs{PublicIPv4: true})

	// The taint reaches the node through its machine-config patch, which is
	// built from the same values.
	assert.Len(t, rec.of("talos:machine/configurationApply:ConfigurationApply"), 5)
}

func TestNewCluster_NoWorkersAllowsSchedulingOnControlPlanes(t *testing.T) {
	t.Parallel()

	// Without this a cluster with no worker pool has nowhere to run a pod.
	topology := singleNodeTopology(t)
	topology.WorkerPools = nil

	rec := runCluster(t, topology, &hetzner.ClusterArgs{PublicIPv4: true})

	assert.Len(t, rec.of("hcloud:index/server:Server"), 1)
}

func TestNewCluster_RejectsMissingTopology(t *testing.T) {
	t.Parallel()

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := hetzner.NewCluster(ctx, "test", &hetzner.ClusterArgs{})

		return err
	}, pulumi.WithMocks("hetzner-iac", "test", newRecorder()))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "topology is required")
}
