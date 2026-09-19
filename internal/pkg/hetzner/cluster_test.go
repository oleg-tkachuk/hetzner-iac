package hetzner_test

import (
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/internals"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec/clusterspectest"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/hetzner"
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

	// serverTypes is what the getServerTypes lookup reports, name to
	// architecture. Empty by default, which ValidateServerTypes reads as
	// "unverified" rather than "none exist".
	serverTypes map[string]string

	// deleteFirst is the deleteBeforeReplace option each resource type was
	// registered with. It comes off the register RPC rather than the inputs,
	// because a resource option is not an input — and this one is the
	// difference between a replacement that works and one that fails.
	deleteFirst map[string]bool

	// protected is the protect option, off the same RPC. A resource option
	// rather than an input, and the one that decides whether `pulumi destroy`
	// can take a resource at all — Hetzner's own DeleteProtection is an input
	// and does not, because the provider clears it before deleting.
	protected map[string]bool

	// replaceOn is the replaceOnChanges option, from the same place and for
	// the same reason.
	replaceOn map[string][]string

	// dependsOn is the URNs each resource type was registered as depending
	// on. Off the register RPC for the same reason as the two above: an
	// ordering constraint is not an input, and this one is the difference
	// between an HA cluster that comes up and an apply that fails after
	// creating everything else.
	dependsOn map[string][]string
}

func newRecorder() *recorder {
	return &recorder{
		resources:   map[string][]resource.PropertyMap{},
		deleteFirst: map[string]bool{},
		protected:   map[string]bool{},
		replaceOn:   map[string][]string{},
		dependsOn:   map[string][]string{},
	}
}

func (r *recorder) record(token string, inputs resource.PropertyMap) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.resources[token] = append(r.resources[token], inputs)
}

// isProtected reports whether a resource type was registered with
// pulumi.Protect.
func (r *recorder) isProtected(token string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.protected[token]
}

func (r *recorder) of(token string) []resource.PropertyMap {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.resources[token]
}

func (r *recorder) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	r.record(args.TypeToken, args.Inputs)

	if rpc := args.RegisterRPC; rpc != nil {
		r.mu.Lock()

		if rpc.GetDeleteBeforeReplaceDefined() {
			r.deleteFirst[args.TypeToken] = rpc.GetDeleteBeforeReplace()
		}

		if rpc.GetProtect() {
			r.protected[args.TypeToken] = true
		}

		if fields := rpc.GetReplaceOnChanges(); len(fields) > 0 {
			r.replaceOn[args.TypeToken] = fields
		}

		if urns := rpc.GetDependencies(); len(urns) > 0 {
			r.dependsOn[args.TypeToken] = urns
		}

		r.mu.Unlock()
	}

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
	case "hcloud:index/getZone:getZone":
		// The zone is looked up, never created — a zone belongs to a
		// delegation that outlives any cluster. The lookup answers with the
		// name it was asked for, which is what the rrsets are anchored to.
		name := ""
		if asked := args.Args["name"]; asked.IsString() {
			name = asked.StringValue()
		}

		return resource.PropertyMap{
			"id":   resource.NewNumberProperty(1),
			"name": resource.NewStringProperty(name),
			"mode": resource.NewStringProperty("primary"),
		}, nil
	case "hcloud:index/getServerTypes:getServerTypes":
		// Empty unless a test sets it. An empty list means "the lookup told us
		// nothing", which ValidateServerTypes treats as unverified rather than
		// as "no type exists" — so every existing test keeps working.
		types := make([]resource.PropertyValue, 0, len(r.serverTypes))
		for name, arch := range r.serverTypes {
			types = append(types, resource.NewObjectProperty(resource.PropertyMap{
				"name":         resource.NewStringProperty(name),
				"architecture": resource.NewStringProperty(arch),
			}))
		}

		return resource.PropertyMap{"serverTypes": resource.NewArrayProperty(types)}, nil
	}

	return resource.PropertyMap{}, nil
}

// runCluster builds a cluster under the mock monitor and returns what was
// registered.
func runCluster(t *testing.T, topology *clusterspec.Topology, args *hetzner.ClusterArgs) *recorder {
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

func haTopology(t *testing.T) *clusterspec.Topology {
	t.Helper()

	return clusterspectest.MustParseValid(t)
}

func singleNodeTopology(t *testing.T) *clusterspec.Topology {
	t.Helper()

	topology := clusterspectest.MustParseValid(t)
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

// TestLoadBalancers_ShareOneSetOfHealthCheckTimings holds the two halves of a
// claim that used to be a comment.
//
// Both load balancers are meant to take a node out of rotation at the same
// rate, and ingress.go said so while the API load balancer carried its own
// 10, 5 and 3. The numbers themselves are not asserted here — a literal
// naming them would be the third copy — only that the two agree.
func TestLoadBalancers_ShareOneSetOfHealthCheckTimings(t *testing.T) {
	t.Parallel()

	api := runCluster(t, haTopology(t), &hetzner.ClusterArgs{PublicIPv4: true}).
		of("hcloud:index/loadBalancerService:LoadBalancerService")
	require.Len(t, api, 1, "the HA control plane has no load balancer service to check")

	ingress := runIngress(t).of("hcloud:index/loadBalancerService:LoadBalancerService")
	require.NotEmpty(t, ingress, "the ingress has no load balancer service to check")

	want := api[0]["healthCheck"].ObjectValue()

	for _, field := range []string{"interval", "timeout", "retries"} {
		require.Positive(t, want[resource.PropertyKey(field)].NumberValue(),
			"the API load balancer's health check has no %s", field)

		for _, service := range ingress {
			check := service["healthCheck"].ObjectValue()

			assert.Equal(t,
				want[resource.PropertyKey(field)].NumberValue(),
				check[resource.PropertyKey(field)].NumberValue(),
				"the ingress health-check %s is %v while the API load balancer's is %v",
				field, check[resource.PropertyKey(field)].NumberValue(),
				want[resource.PropertyKey(field)].NumberValue())
		}
	}
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

func TestCluster_ServerReplacementDeletesFirst(t *testing.T) {
	t.Parallel()

	// A Hetzner server name is unique within the project, so Pulumi's default
	// create-before-delete cannot replace one: the create is rejected before
	// the delete runs, and the stack errors with the old server still
	// standing.
	//
	//     server name is already used (uniqueness_error)
	//
	// Measured on a deliberate replacement of the only control-plane node,
	// which failed in five seconds having changed nothing. Every input that
	// forces a replacement reaches this — the server type, the datacenter, the
	// private address — so it is worth a test rather than a comment.
	rec := runCluster(t, singleNodeTopology(t), &hetzner.ClusterArgs{})

	deleteFirst, ok := rec.deleteFirst["hcloud:index/server:Server"]
	require.True(t, ok, "the server does not set deleteBeforeReplace at all")
	assert.True(t, deleteFirst, "a replacement would fail on the unique server name")
}

func TestCluster_BootstrapIsReplacedWhenTheNodeChanges(t *testing.T) {
	t.Parallel()

	// A bootstrap is one-shot, and the provider declares `node` as an
	// updatable field — so replacing the first control-plane server produced
	// an UPDATE: the address was rewritten in state, nothing ran, and the new
	// node sat there saying
	//
	//     etcd is waiting to join the cluster … please run `talosctl bootstrap`
	//
	// while the apply reported success. A green apply and a cluster with no
	// etcd is the worst shape this failure can take, which is why the option
	// is pinned here rather than trusted to a comment.
	rec := runCluster(t, singleNodeTopology(t), &hetzner.ClusterArgs{})

	assert.Equal(t, []string{"node"}, rec.replaceOn["talos:machine/bootstrap:Bootstrap"],
		"a new node address must replace the bootstrap, or it is never performed")
}

func TestNewCluster_TheAPITargetWaitsForTheLoadBalancerToJoinTheNetwork(t *testing.T) {
	t.Parallel()

	rec := runCluster(t, haTopology(t), &hetzner.ClusterArgs{PublicIPv4: true})

	depends := rec.dependsOn["hcloud:index/loadBalancerTarget:LoadBalancerTarget"]
	require.NotEmpty(t, depends, "the api target was registered with no dependencies at all")

	var onAttachment bool

	for _, urn := range depends {
		if strings.Contains(urn, "loadBalancerNetwork:LoadBalancerNetwork") {
			onAttachment = true
		}
	}

	// The target uses the private address, and Hetzner refuses one on a load
	// balancer that is not in a network yet. With only the subnet in the list
	// Pulumi created the two in parallel and the first real HA apply failed
	// with `load_balancer_not_attached_to_network` — after creating
	// everything else.
	assert.True(t, onAttachment,
		"the api target does not wait for the load balancer's network attachment")
}

// TestNewCluster_MarksBothCredentialsSecret is the property asSecret exists to
// guarantee, and nothing asserted it before.
//
// Both of these are cluster-admin. An output that loses its secret flag is
// printed in full by `pulumi preview`, in the diff of whatever consumes it, and
// again by `pulumi stack export` with no `--show-secrets` — and talosconfig is
// the more powerful of the two, because it can reset a node and read etcd.
func TestNewCluster_MarksBothCredentialsSecret(t *testing.T) {
	t.Parallel()

	require.NoError(t, pulumi.RunErr(func(ctx *pulumi.Context) error {
		cluster, err := hetzner.NewCluster(ctx, "test", &hetzner.ClusterArgs{
			Topology:   clusterspectest.MustParseValid(t),
			PublicIPv4: true,
		})
		require.NoError(t, err)

		for name, output := range map[string]pulumi.StringOutput{
			"kubeconfig":  cluster.Kubeconfig,
			"talosconfig": cluster.Talosconfig,
		} {
			result, awaitErr := internals.UnsafeAwaitOutput(ctx.Context(), output)
			require.NoError(t, awaitErr, name)

			assert.True(t, result.Secret, "%s is cluster-admin and is not marked secret", name)
		}

		return nil
	}, pulumi.WithMocks("hetzner-iac", "test", newRecorder())))
}
