package hetzner_test

import (
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/internals"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"

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

	// calls is the arguments of every invoke, keyed the same way. The machine
	// configuration is an invoke rather than a resource, and the patch every
	// node shares goes in through its arguments, so a setting in that patch
	// is visible nowhere else.
	calls map[string][]resource.PropertyMap

	// serverTypes is what the getServerTypes lookup reports, name to
	// architecture. Empty by default, which ValidateServerTypes reads as
	// "unverified" rather than "none exist".
	serverTypes map[string]string

	// deleteFirst is the deleteBeforeReplace option each resource type was
	// registered with. It comes off the register RPC rather than the inputs,
	// because a resource option is not an input — and this one is the
	// difference between a replacement that works and one that fails.
	deleteFirst map[string]bool

	// protectedNames is the protect option, off the same RPC. A resource
	// option rather than an input, and the one that decides whether Pulumi can
	// delete or replace a resource at all — Hetzner's own DeleteProtection is
	// an input and does not, because the provider clears it before deleting.
	//
	// Keyed by NAME rather than by type: a worker and a control-plane node are
	// both hcloud:index/server:Server, and telling those apart is exactly what
	// the assertions need.
	protectedNames map[string]bool

	// replaceOn is the replaceOnChanges option, from the same place and for
	// the same reason.
	replaceOn map[string][]string

	// dependsOn is the URNs each resource type was registered as depending
	// on. Off the register RPC for the same reason as the two above: an
	// ordering constraint is not an input, and this one is the difference
	// between an HA cluster that comes up and an apply that fails after
	// creating everything else.
	dependsOn map[string][]string

	// replaceWith is the URNs each resource, by name, is replaced with.
	replaceWith map[string][]string

	// ignoreChanges is the properties each resource, by name, ignores.
	ignoreChanges map[string][]string

	// callProviders is the provider reference each function call was made
	// through, by token. Empty when the call went to the default provider —
	// the one configured from the ambient environment rather than the one a
	// layer built from its own token.
	callProviders map[string]string
}

const (
	// testLoadBalancerPublicIP is the address Hetzner gives a load balancer
	// whether or not its public interface is enabled.
	testLoadBalancerPublicIP = "203.0.113.200"
	// testLoadBalancerPrivateIP is the load balancer's address in the network.
	testLoadBalancerPrivateIP = "10.0.1.250"
	// testFirstNodeIP is the public address every mock server reports.
	testFirstNodeIP = "203.0.113.10"
	// testTalosImageID is the snapshot every image lookup answers with.
	testTalosImageID = 4242
	// talosKubeconfig is a kubeconfig as Talos returns it, pointed at the
	// cluster endpoint.
	talosKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: https://` + testLoadBalancerPrivateIP + `:6443
    certificate-authority-data: Y2E=
users:
- name: admin@test
  user:
    client-certificate-data: Y2VydA==
    client-key-data: a2V5
contexts:
- name: admin@test
  context:
    cluster: test
    user: admin@test
current-context: admin@test
`
)

func newRecorder() *recorder {
	return &recorder{
		resources:      map[string][]resource.PropertyMap{},
		calls:          map[string][]resource.PropertyMap{},
		protectedNames: map[string]bool{},
		deleteFirst:    map[string]bool{},
		replaceOn:      map[string][]string{},
		dependsOn:      map[string][]string{},
		replaceWith:    map[string][]string{},
		ignoreChanges:  map[string][]string{},
		callProviders:  map[string]string{},
	}
}

// providerOf is the provider reference the call with this token went through.
func (r *recorder) providerOf(token string) string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.callProviders[token]
}

func (r *recorder) record(token string, inputs resource.PropertyMap) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.resources[token] = append(r.resources[token], inputs)
}

// isProtected reports whether the resource with this name was registered with
// pulumi.Protect.
func (r *recorder) isProtected(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.protectedNames[name]
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
			r.protectedNames[args.Name] = true
		}

		if fields := rpc.GetReplaceOnChanges(); len(fields) > 0 {
			r.replaceOn[args.TypeToken] = fields
		}

		if urns := rpc.GetDependencies(); len(urns) > 0 {
			r.dependsOn[args.TypeToken] = urns
		}

		if urns := rpc.GetReplaceWith(); len(urns) > 0 {
			r.replaceWith[args.Name] = urns
		}

		if fields := rpc.GetIgnoreChanges(); len(fields) > 0 {
			r.ignoreChanges[args.Name] = fields
		}

		r.mu.Unlock()
	}

	outputs := args.Inputs.Copy()

	switch args.TypeToken {
	case "hcloud:index/server:Server":
		// A real server reports its public address only after creation; the
		// components turn that output into certificate SANs, so it has to be
		// present for the graph to resolve.
		outputs["ipv4Address"] = resource.NewStringProperty(testFirstNodeIP)
	case "hcloud:index/loadBalancer:LoadBalancer":
		outputs["ipv4"] = resource.NewStringProperty(testLoadBalancerPublicIP)
	case "hcloud:index/loadBalancerNetwork:LoadBalancerNetwork":
		outputs["ip"] = resource.NewStringProperty(testLoadBalancerPrivateIP)
	case "talos:cluster/kubeconfig:Kubeconfig":
		// What Talos writes: the server is the cluster endpoint.
		outputs["kubeconfigRaw"] = resource.NewStringProperty(talosKubeconfig)
	}

	// Numeric ids: hcloud ids are always numeric and the components parse
	// them, so a non-numeric mock id would fail for the wrong reason.
	return "1", outputs, nil
}

// callsOf is every invoke of this token, by its arguments.
func (r *recorder) callsOf(token string) []resource.PropertyMap {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.calls[token]
}

func (r *recorder) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	r.mu.Lock()
	r.calls[args.Token] = append(r.calls[args.Token], args.Args)
	r.callProviders[args.Token] = args.Provider
	r.mu.Unlock()

	switch args.Token {
	case "hcloud:index/getImage:getImage":
		return resource.PropertyMap{
			"id": resource.NewNumberProperty(testTalosImageID),
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

// workerAddresses is every worker's private address, by node name.
func workerAddresses(rec *recorder) map[string]string {
	out := map[string]string{}

	for _, server := range rec.of("hcloud:index/server:Server") {
		name := server["name"].StringValue()
		if strings.Contains(name, clusterspec.RoleControlPlane) {
			continue
		}

		out[name] = server["networks"].ArrayValue()[0].ObjectValue()["ip"].StringValue()
	}

	return out
}

func TestNewCluster_RemovingAPoolLeavesAPinnedOneWhereItWas(t *testing.T) {
	t.Parallel()

	slot := 1
	general := clusterspec.WorkerPoolSpec{Name: "general", Count: 1, ServerType: "cx33"}
	gpu := clusterspec.WorkerPoolSpec{Name: "gpu", Count: 1, ServerType: "cx33", AddressSlot: &slot}

	before := haTopology(t)
	before.WorkerPools = []clusterspec.WorkerPoolSpec{general, gpu}

	after := haTopology(t)
	after.WorkerPools = []clusterspec.WorkerPoolSpec{gpu}

	gpuNode := clusterspec.NodeName(before.Metadata.Name, gpu.Name, 0)

	// Unpinned, gpu would move to the first worker slice once general is
	// gone, and its node would be replaced with a new address.
	assert.Equal(t,
		workerAddresses(runCluster(t, before, &hetzner.ClusterArgs{PublicIPv4: true}))[gpuNode],
		workerAddresses(runCluster(t, after, &hetzner.ClusterArgs{PublicIPv4: true}))[gpuNode])
}

func TestNewCluster_ControlPlanesKeepTheirAddressAcrossAReplacement(t *testing.T) {
	t.Parallel()

	// A server's implicit address is deleted with it, servers are replaced
	// delete-first, and the kubeconfig and talosconfig name the first
	// control-plane node's address. An explicit Primary IP outlives the server.
	topology := haTopology(t)
	rec := runCluster(t, topology, &hetzner.ClusterArgs{PublicIPv4: true})

	addresses := nodeAddresses(rec)
	require.Len(t, addresses, topology.ControlPlane.Count, "one per control-plane node, none for workers")

	for _, address := range addresses {
		assert.False(t, address["autoDelete"].BoolValue(), "an auto-deleted address goes with its server")
		assert.Equal(t, topology.Placement.Location, address["location"].StringValue())
		assert.True(t, rec.isProtected(address["name"].StringValue()+"-ipv4"), "protected with its server")
		assert.NotContains(t, address, resource.PropertyKey("assigneeType"),
			"the provider wants assigneeType only beside assigneeId, and warns otherwise")
		assert.Contains(t, rec.ignoreChanges[address["name"].StringValue()+"-ipv4"], "assigneeId",
			"the server assigns the address; left to the address, a read-back assignment plans an unassignment")
	}

	for _, server := range rec.of("hcloud:index/server:Server") {
		name := server["name"].StringValue()
		ipv4 := server["publicNets"].ArrayValue()[0].ObjectValue()["ipv4"]

		if strings.Contains(name, clusterspec.RoleControlPlane) {
			assert.False(t, ipv4.IsNull(), "%s is not given its Primary IP", name)
			// The provider updates a public network by powering the server
			// off and, from an implicit address, deleting it first.
			assert.Contains(t, rec.ignoreChanges[name], "publicNets", "%s's public network is updatable", name)

			continue
		}

		assert.True(t, ipv4.IsNull(), "%s is a worker, replaceable on any address", name)
	}
}

func TestNewCluster_NoNodePrimaryIPWithoutAPublicAddress(t *testing.T) {
	t.Parallel()

	rec := runCluster(t, haTopology(t), &hetzner.ClusterArgs{PublicIPv4: false})

	assert.Empty(t, nodeAddresses(rec))
}

// ingressSuffix is what the ingress Primary IPs' names end in before their
// type, and what tells them apart from a node's.
const ingressSuffix = "-ingress-"

// nodeAddresses is every Primary IP a node holds, without the ingress's.
func nodeAddresses(rec *recorder) []resource.PropertyMap {
	var nodes []resource.PropertyMap

	for _, address := range rec.of("hcloud:index/primaryIp:PrimaryIp") {
		if !strings.Contains(address["name"].StringValue(), ingressSuffix) {
			nodes = append(nodes, address)
		}
	}

	return nodes
}

// TestNewCluster_KeepsTheIngressAddresses holds the property the ingress
// depends on: its two addresses outlive the load balancer, so a replaced one
// comes back where the domain's records point.
func TestNewCluster_KeepsTheIngressAddresses(t *testing.T) {
	t.Parallel()

	// Without node addresses too: the ingress is public whatever the nodes are.
	for _, publicIPv4 := range []bool{true, false} {
		topology := haTopology(t)
		rec := runCluster(t, topology, &hetzner.ClusterArgs{PublicIPv4: publicIPv4})

		byType := map[string]resource.PropertyMap{}

		for _, address := range rec.of("hcloud:index/primaryIp:PrimaryIp") {
			if strings.Contains(address["name"].StringValue(), ingressSuffix) {
				byType[address["type"].StringValue()] = address
			}
		}

		require.Len(t, byType, 2, "one IPv4 and one IPv6 for the ingress, publicIPv4=%t", publicIPv4)

		for kind, address := range byType {
			resourceName := "test" + ingressSuffix + kind

			assert.Equal(t, topology.Metadata.Name+ingressSuffix+kind, address["name"].StringValue())
			assert.False(t, address["autoDelete"].BoolValue(),
				"an auto-deleted address goes with the load balancer it is assigned to")
			assert.Equal(t, topology.Placement.Location, address["location"].StringValue(),
				"a Primary IP is assignable only in its own location")
			assert.True(t, rec.isProtected(resourceName), "%s is not protected", kind)
			assert.NotContains(t, address, resource.PropertyKey("assigneeType"),
				"the load balancer assigns the address at creation")
			assert.Subset(t, rec.ignoreChanges[resourceName], []string{"assigneeId", "assigneeType"},
				"left to the address, a read-back assignment plans an unassignment")
		}
	}
}

func TestNewCluster_GeneratesConfigForTheContractAndBootsTheVersion(t *testing.T) {
	t.Parallel()

	// A cluster upgraded to 1.14 keeps generating 1.13-shaped configuration:
	// 1.14 rejects that shape once it is generated for 1.14, and the generator
	// here cannot produce 1.14's documents.
	contract := "v1.13.10"
	topology := haTopology(t)
	topology.Talos.Version = "v1.14.2"
	topology.Talos.ConfigVersion = &contract

	rec := runCluster(t, topology, &hetzner.ClusterArgs{PublicIPv4: true})

	for _, config := range rec.callsOf("talos:machine/getConfiguration:getConfiguration") {
		assert.Equal(t, contract, config["talosVersion"].StringValue())
	}

	secrets := rec.of("talos:machine/secrets:Secrets")
	require.Len(t, secrets, 1)
	assert.Equal(t, contract, secrets[0]["talosVersion"].StringValue())

	images := rec.callsOf("hcloud:index/getImage:getImage")
	require.NotEmpty(t, images)
	assert.Contains(t, images[0]["withSelector"].StringValue(), "v1.14.2", "the nodes boot the version they run")
}

func TestNewCluster_TheSecretsBundleNeverFollowsAVersionChange(t *testing.T) {
	t.Parallel()

	// Its talosVersion is read when the CA is generated. Reaching it later
	// plans a replacement of the CA, which only Protect would refuse.
	rec := runCluster(t, haTopology(t), &hetzner.ClusterArgs{PublicIPv4: true})

	var name string

	for candidate := range rec.ignoreChanges {
		if strings.HasSuffix(candidate, "-secrets") {
			name = candidate
		}
	}

	require.NotEmpty(t, name, "the secrets bundle ignores nothing")
	assert.Contains(t, rec.ignoreChanges[name], "talosVersion")
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

	applies := rec.of("talos:machine/configurationApply:ConfigurationApply")
	assert.Len(t, applies, 5)

	// The labels and taints reach the node through its machine-config patch,
	// and only where Talos reads them: machine.nodeLabels and
	// machine.nodeTaints. Counting the applies alone passed while both were
	// written under machine.kubelet, which Talos refuses as unknown keys.
	hostname := clusterspec.NodeName(topology.Metadata.Name, topology.WorkerPools[0].Name, 0)

	var machine map[string]any

	for _, apply := range applies {
		for _, patch := range apply["configPatches"].ArrayValue() {
			if !strings.Contains(patch.StringValue(), "hostname: "+hostname) {
				continue
			}

			first, _, _ := strings.Cut(patch.StringValue(), "\n---\n")

			var doc map[string]any
			require.NoError(t, yaml.Unmarshal([]byte(first), &doc))

			machine, _ = doc["machine"].(map[string]any)
		}
	}

	require.NotNil(t, machine, "no node patch for %s", hostname)
	assert.NotContains(t, machine, "kubelet")
	assert.Equal(t, map[string]any{clusterspec.LabelPool: topology.WorkerPools[0].Name, "workload": "gpu"},
		machine["nodeLabels"])
	assert.Equal(t, map[string]any{"gpu": "true:NoSchedule"}, machine["nodeTaints"])
}

// allowSchedulingKey is the Talos cluster setting that lets pods run on a
// control-plane node.
const allowSchedulingKey = "allowSchedulingOnControlPlanes"

// schedulingOnControlPlanes reads allowSchedulingOnControlPlanes out of every
// machine-config patch that sets it, which is the value Talos is handed rather
// than the argument NewCluster computed.
//
// From the machine-configuration invoke, not the ConfigurationApply: the
// shared cluster patch is generated into the configuration there, and each
// apply carries only its own node's patch.
func schedulingOnControlPlanes(t *testing.T, rec *recorder) []any {
	t.Helper()

	var found []any

	for _, generated := range rec.callsOf("talos:machine/getConfiguration:getConfiguration") {
		for _, patch := range generated["configPatches"].ArrayValue() {
			first, _, _ := strings.Cut(patch.StringValue(), "\n---\n")

			var doc map[string]any
			require.NoError(t, yaml.Unmarshal([]byte(first), &doc))

			cluster, _ := doc["cluster"].(map[string]any)
			if value, set := cluster[allowSchedulingKey]; set {
				found = append(found, value)
			}
		}
	}

	return found
}

func TestNewCluster_NoWorkersAllowsSchedulingOnControlPlanes(t *testing.T) {
	t.Parallel()

	// Without this a cluster with no worker pool has nowhere to run a pod.
	topology := singleNodeTopology(t)
	topology.WorkerPools = nil

	rec := runCluster(t, topology, &hetzner.ClusterArgs{PublicIPv4: true})

	assert.Len(t, rec.of("hcloud:index/server:Server"), 1)

	// Counting servers alone passed whatever the setting said, and the setting
	// is the whole claim: read it from the patch the node is configured with.
	allowed := schedulingOnControlPlanes(t, rec)
	require.NotEmpty(t, allowed, "no machine-config patch sets %s", allowSchedulingKey)

	for _, value := range allowed {
		assert.Equal(t, true, value, "a cluster with no workers must schedule on its control plane")
	}
}

func TestNewCluster_WorkersKeepPodsOffTheControlPlanes(t *testing.T) {
	t.Parallel()

	// The opposite, so a setting stuck at true cannot pass the test above: with
	// a worker pool, workloads belong on the workers.
	rec := runCluster(t, haTopology(t), &hetzner.ClusterArgs{PublicIPv4: true})

	allowed := schedulingOnControlPlanes(t, rec)
	require.NotEmpty(t, allowed, "no machine-config patch sets %s", allowSchedulingKey)

	for _, value := range allowed {
		assert.Equal(t, false, value, "a cluster with workers must keep pods off its control plane")
	}
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

// replacedWith is the one resource the named resource is replaced with.
func (r *recorder) replacedWith(t *testing.T, name string) string {
	t.Helper()

	r.mu.Lock()
	defer r.mu.Unlock()

	require.Len(t, r.replaceWith[name], 1, "%s is not replaced with exactly one resource", name)

	return r.replaceWith[name][0]
}

func TestCluster_TalosIsRedoneWhenItsServerIsReplaced(t *testing.T) {
	t.Parallel()

	// With no public address a node keeps its private IP across a
	// replacement, so no input of the apply or the bootstrap changes. The
	// new server would sit in maintenance mode, unconfigured and with no
	// etcd, while the apply reported success.
	rec := runCluster(t, haTopology(t), &hetzner.ClusterArgs{PublicIPv4: false})

	applies := 0
	servers := map[string]bool{}

	for name := range rec.replaceWith {
		if !strings.Contains(name, "-config-") {
			continue
		}

		server := rec.replacedWith(t, name)
		assert.Contains(t, server, "hcloud:index/server:Server", name)
		assert.False(t, servers[server], "two applies are tied to %s", server)

		servers[server] = true
		applies++
	}

	assert.Equal(t, len(rec.of("talos:machine/configurationApply:ConfigurationApply")), applies,
		"every configuration apply is redone with its server")

	assert.Equal(t, rec.replacedWith(t, "test-control-plane-config-0"), rec.replacedWith(t, "test-control-plane-bootstrap"),
		"the bootstrap is redone with the first control-plane server")
}

// TestNewCluster_TheAPILoadBalancerIsPrivate pins item 4 of the audit: a
// Hetzner firewall cannot be attached to a load balancer, so a public
// interface on it opened 6443 to everyone beside a firewall admitting only
// network.adminCIDRs. The nodes use its private address; operators use a node.
func TestNewCluster_TheAPILoadBalancerIsPrivate(t *testing.T) {
	t.Parallel()

	rec := runCluster(t, haTopology(t), &hetzner.ClusterArgs{PublicIPv4: true})

	attachments := rec.of("hcloud:index/loadBalancerNetwork:LoadBalancerNetwork")
	require.Len(t, attachments, 1)

	public := attachments[0]["enablePublicInterface"]
	require.True(t, public.IsBool(), "the public interface is left at Hetzner's default, which is on")
	assert.False(t, public.BoolValue())

	privateEndpoint := "https://" + testLoadBalancerPrivateIP + ":6443"

	configs := rec.callsOf("talos:machine/getConfiguration:getConfiguration")
	require.NotEmpty(t, configs)

	for _, config := range configs {
		assert.Equal(t, privateEndpoint, config["clusterEndpoint"].StringValue(),
			"the nodes reach the API through the load balancer's private address")
	}
}

// serviceAccountIssuers is the issuer list in each node patch, by hostname.
func serviceAccountIssuers(t *testing.T, rec *recorder) map[string]any {
	t.Helper()

	out := map[string]any{}

	for _, apply := range rec.of("talos:machine/configurationApply:ConfigurationApply") {
		for _, patch := range apply["configPatches"].ArrayValue() {
			first, _, _ := strings.Cut(patch.StringValue(), "\n---\n")

			var doc map[string]any
			require.NoError(t, yaml.Unmarshal([]byte(first), &doc))

			cluster, _ := doc["cluster"].(map[string]any)
			apiServer, _ := cluster["apiServer"].(map[string]any)
			extraArgs, _ := apiServer["extraArgs"].(map[string]any)

			out[patch.StringValue()] = extraArgs["service-account-issuer"]
		}
	}

	return out
}

func TestNewCluster_ControlPlanesSignWithAFixedIssuer(t *testing.T) {
	t.Parallel()

	// Talos derives the issuer from the cluster endpoint, so moving the
	// endpoint off the public load balancer made every token in every pod
	// fail with 401.
	topology := haTopology(t)
	rec := runCluster(t, topology, &hetzner.ClusterArgs{PublicIPv4: true})

	want := clusterspec.ServiceAccountIssuer
	controlPlanes, workers := 0, 0

	for patch, issuers := range serviceAccountIssuers(t, rec) {
		if strings.Contains(patch, clusterspec.RoleControlPlane) {
			assert.Equal(t, want, issuers)

			controlPlanes++

			continue
		}

		assert.Nil(t, issuers, "a worker runs no kube-apiserver")

		workers++
	}

	assert.Equal(t, topology.ControlPlane.Count, controlPlanes)
	assert.Positive(t, workers)
}

func TestNewCluster_TheKubeconfigReachesTheFirstNode(t *testing.T) {
	t.Parallel()

	clientEndpoint := "https://" + testFirstNodeIP + ":6443"
	topology := haTopology(t)

	require.NoError(t, pulumi.RunErr(func(ctx *pulumi.Context) error {
		cluster, err := hetzner.NewCluster(ctx, "test", &hetzner.ClusterArgs{
			Topology:   topology,
			PublicIPv4: true,
		})
		require.NoError(t, err)

		kubeconfig, err := internals.UnsafeAwaitOutput(ctx.Context(), cluster.Kubeconfig)
		require.NoError(t, err)

		config, err := clientcmd.Load([]byte(kubeconfig.Value.(string)))
		require.NoError(t, err)

		current := config.Contexts[config.CurrentContext]
		require.NotNil(t, current)
		assert.Equal(t, clientEndpoint, config.Clusters[current.Cluster].Server,
			"the current context points at the load balancer's private address, which no operator can reach")
		assert.Len(t, config.Contexts, topology.ControlPlane.Count, "one context per control-plane node")

		for name, entry := range config.Clusters {
			assert.Equal(t, []byte("ca"), entry.CertificateAuthorityData, "%s keeps the CA", name)
		}

		endpoint, err := internals.UnsafeAwaitOutput(ctx.Context(), cluster.Endpoint)
		require.NoError(t, err)
		assert.Equal(t, clientEndpoint, endpoint.Value, "the exported endpoint is the one the kubeconfig uses")

		return nil
	}, pulumi.WithMocks("hetzner-iac", "test", newRecorder())))
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

// TestNewCluster_PublishesTheImageANewNodeBoots holds the component output to
// the lookup the servers are created from, rather than to any other id.
func TestNewCluster_PublishesTheImageANewNodeBoots(t *testing.T) {
	t.Parallel()

	rec := newRecorder()
	want := strconv.Itoa(testTalosImageID)

	require.NoError(t, pulumi.RunErr(func(ctx *pulumi.Context) error {
		cluster, err := hetzner.NewCluster(ctx, "test", &hetzner.ClusterArgs{
			Topology:   clusterspectest.MustParseValid(t),
			PublicIPv4: true,
		})
		require.NoError(t, err)

		image, err := internals.UnsafeAwaitOutput(ctx.Context(), cluster.TalosImage)
		require.NoError(t, err)

		assert.Equal(t, want, image.Value)

		return nil
	}, pulumi.WithMocks("hetzner-iac", "test", rec)))

	servers := rec.of("hcloud:index/server:Server")
	require.NotEmpty(t, servers)

	for _, server := range servers {
		assert.Equal(t, want, server["image"].StringValue(),
			"%s boots an image other than the one published", server["name"].StringValue())
	}
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

// TestNewCluster_ProtectsTheControlPlaneAndTheEndpoint is the assertion that
// replaces a comment.
//
// server.go said an HA replacement happens "one member at a time, which etcd
// survives". Nothing enforced it, and the live stack says otherwise: the three
// control-plane servers depend on each other not at all, so the engine may act
// on all three at once — `--parallel` defaults to 56 — and DeleteBeforeReplace
// means each is deleted before its replacement exists. A single edit of
// `placement.location` plans exactly that.
//
// Read off the register RPC, because protect is a resource OPTION and not an
// input. The names matter as much as the count: a worker is the same resource
// type and is replaceable by design, so protecting it would make a refusal
// fire on ordinary work, and a refusal that fires on ordinary work gets
// bypassed by habit.
func TestNewCluster_ProtectsTheControlPlaneAndTheEndpoint(t *testing.T) {
	t.Parallel()

	rec := runCluster(t, haTopology(t), &hetzner.ClusterArgs{PublicIPv4: true})

	for _, name := range []string{
		"platform-hel-control-plane-0",
		"platform-hel-control-plane-1",
		"platform-hel-control-plane-2",
	} {
		assert.True(t, rec.isProtected(name),
			"%s is not protected, so one topology edit can replace every control-plane node "+
				"at once and etcd goes with them", name)
	}

	// "test" is the component's own Pulumi name here, where a server's name
	// comes from the topology — which is why these two read differently.
	assert.True(t, rec.isProtected("test-api"),
		"the API load balancer is not protected; its address is the cluster endpoint every "+
			"certificate names, and a replacement hands back a different one")

	assert.True(t, rec.isProtected("test-secrets"),
		"the Talos secrets bundle is the cluster CA and has been protected since it existed")

	for _, name := range []string{"platform-hel-worker-0", "platform-hel-worker-1"} {
		assert.False(t, rec.isProtected(name),
			"%s is protected, and workers are replaceable by design: a refusal that fires on "+
				"ordinary work is one that gets bypassed by habit", name)
	}
}
