package clusterspec_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
)

// decode parses the FIRST document of a rendered patch, so assertions are made
// against structure rather than against substrings of YAML — a substring check
// passes on a document whose nesting is wrong.
func decode(t *testing.T, patch string) map[string]any {
	t.Helper()

	var out map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(documents(t, patch)[0]), &out))

	return out
}

// documents splits a multi-document patch. A node patch carries two: the
// v1alpha1 machine config, and a HostnameConfig document.
func documents(t *testing.T, patch string) []string {
	t.Helper()

	parts := strings.Split(patch, "---\n")
	require.NotEmpty(t, parts)

	return parts
}

// hostnameDoc decodes the HostnameConfig document of a node patch.
func hostnameDoc(t *testing.T, patch string) map[string]any {
	t.Helper()

	docs := documents(t, patch)
	require.Len(t, docs, 2, "a node patch carries the machine config and a HostnameConfig document")

	var out map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(docs[1]), &out))

	return out
}

func TestBuildClusterPatch(t *testing.T) {
	t.Parallel()

	patch, err := clusterspec.BuildClusterPatch(clusterspec.ClusterPatchArgs{
		PodCIDR:     "10.244.0.0/16",
		ServiceCIDR: "10.96.0.0/12",
		NodeSubnet:  "10.0.1.0/24",
		IPRange:     "10.0.0.0/16",
	})
	require.NoError(t, err)

	doc := decode(t, patch)
	cluster, _ := doc["cluster"].(map[string]any)
	network, _ := cluster["network"].(map[string]any)

	assert.Equal(t, []any{"10.244.0.0/16"}, network["podSubnets"])
	assert.Equal(t, []any{"10.96.0.0/12"}, network["serviceSubnets"])
}

func TestBuildClusterPatch_LeavesTheCNIToItsOwnLayer(t *testing.T) {
	t.Parallel()

	// Talos installs Flannel unless told otherwise. Shipping with a CNI the
	// layers/10-node-platform would then have to remove is worse than shipping
	// one: nodes stay NotReady until that layer runs, which is visible and
	// intended, rather than two CNIs briefly fighting.
	patch, err := clusterspec.BuildClusterPatch(clusterspec.ClusterPatchArgs{
		PodCIDR: "10.244.0.0/16", ServiceCIDR: "10.96.0.0/12", NodeSubnet: "10.0.1.0/24",
		IPRange: "10.0.0.0/16",
	})
	require.NoError(t, err)

	doc := decode(t, patch)
	cluster, _ := doc["cluster"].(map[string]any)
	network, _ := cluster["network"].(map[string]any)
	cni, _ := network["cni"].(map[string]any)

	assert.Equal(t, "none", cni["name"])
}

func TestBuildClusterPatch_DisablesKubeProxyForCilium(t *testing.T) {
	t.Parallel()

	// Cilium replaces kube-proxy in eBPF. Leaving kube-proxy enabled means
	// two components programming the same service dataplane.
	patch, err := clusterspec.BuildClusterPatch(clusterspec.ClusterPatchArgs{
		PodCIDR: "10.244.0.0/16", ServiceCIDR: "10.96.0.0/12", NodeSubnet: "10.0.1.0/24",
		IPRange: "10.0.0.0/16",
	})
	require.NoError(t, err)

	doc := decode(t, patch)
	cluster, _ := doc["cluster"].(map[string]any)
	proxy, _ := cluster["proxy"].(map[string]any)

	assert.Equal(t, true, proxy["disabled"])
}

func TestBuildClusterPatch_HandsNodeLifecycleToTheCCM(t *testing.T) {
	t.Parallel()

	// cloud-provider=external is what leaves nodes carrying the
	// `uninitialized` taint until the hcloud CCM starts — the mechanism that
	// stops workloads landing on a node before its routes exist.
	patch, err := clusterspec.BuildClusterPatch(clusterspec.ClusterPatchArgs{
		PodCIDR: "10.244.0.0/16", ServiceCIDR: "10.96.0.0/12", NodeSubnet: "10.0.1.0/24",
		IPRange: "10.0.0.0/16",
	})
	require.NoError(t, err)

	doc := decode(t, patch)

	machine, _ := doc["machine"].(map[string]any)
	kubelet, _ := machine["kubelet"].(map[string]any)
	kubeletArgs, _ := kubelet["extraArgs"].(map[string]any)
	assert.Equal(t, "external", kubeletArgs["cloud-provider"])

	// The controller manager, and only the controller manager. This test used
	// to assert the API server carried the flag too, which is how the bug
	// survived review: the test encoded it rather than catching it. See
	// TestBuildClusterPatch_DoesNotPassCloudProviderToTheAPIServer.
	cluster, _ := doc["cluster"].(map[string]any)
	controllerManager, _ := cluster["controllerManager"].(map[string]any)
	args, _ := controllerManager["extraArgs"].(map[string]any)
	assert.Equal(t, "external", args["cloud-provider"])
}

func TestBuildClusterPatch_PinsKubeletToThePrivateNetwork(t *testing.T) {
	t.Parallel()

	// Without validSubnets a node with a public address advertises it, and
	// every intra-cluster connection then leaves the private network —
	// metered, and exposed.
	patch, err := clusterspec.BuildClusterPatch(clusterspec.ClusterPatchArgs{
		PodCIDR: "10.244.0.0/16", ServiceCIDR: "10.96.0.0/12", NodeSubnet: "10.0.1.0/24",
		IPRange: "10.0.0.0/16",
	})
	require.NoError(t, err)

	doc := decode(t, patch)
	machine, _ := doc["machine"].(map[string]any)
	kubelet, _ := machine["kubelet"].(map[string]any)
	nodeIP, _ := kubelet["nodeIP"].(map[string]any)

	assert.Equal(t, []any{"10.0.1.0/24"}, nodeIP["validSubnets"])
}

func TestBuildClusterPatch_SchedulingOnControlPlanes(t *testing.T) {
	t.Parallel()

	for _, allow := range []bool{true, false} {
		patch, err := clusterspec.BuildClusterPatch(clusterspec.ClusterPatchArgs{
			PodCIDR: "10.244.0.0/16", ServiceCIDR: "10.96.0.0/12", NodeSubnet: "10.0.1.0/24",
			IPRange:                        "10.0.0.0/16",
			AllowSchedulingOnControlPlanes: allow,
		})
		require.NoError(t, err)

		doc := decode(t, patch)
		cluster, _ := doc["cluster"].(map[string]any)
		assert.Equal(t, allow, cluster["allowSchedulingOnControlPlanes"])
	}
}

func TestBuildClusterPatch_Rejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    clusterspec.ClusterPatchArgs
		wantMsg string
	}{
		{
			name:    "no pod CIDR",
			args:    clusterspec.ClusterPatchArgs{ServiceCIDR: "10.96.0.0/12", NodeSubnet: "10.0.1.0/24", IPRange: "10.0.0.0/16"},
			wantMsg: "podCIDR and serviceCIDR are required",
		},
		{
			name:    "no service CIDR",
			args:    clusterspec.ClusterPatchArgs{PodCIDR: "10.244.0.0/16", NodeSubnet: "10.0.1.0/24", IPRange: "10.0.0.0/16"},
			wantMsg: "podCIDR and serviceCIDR are required",
		},
		{
			name:    "no node subnet",
			args:    clusterspec.ClusterPatchArgs{PodCIDR: "10.244.0.0/16", ServiceCIDR: "10.96.0.0/12", IPRange: "10.0.0.0/16"},
			wantMsg: "nodeSubnet is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := clusterspec.BuildClusterPatch(tc.args)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantMsg)
		})
	}
}

func TestBuildNodePatch(t *testing.T) {
	t.Parallel()

	patch, err := clusterspec.BuildNodePatch(clusterspec.NodePatchArgs{
		Hostname: "platform-hel-control-plane-0",
		CertSANs: []string{"203.0.113.10", "10.0.1.2", "203.0.113.99"},
	})
	require.NoError(t, err)

	doc := decode(t, patch)
	machine, _ := doc["machine"].(map[string]any)

	assert.Equal(t, []any{"203.0.113.10", "10.0.1.2", "203.0.113.99"}, machine["certSANs"])

	// The hostname lives in its own document: Talos refuses
	// machine.network.hostname while a HostnameConfig document exists, which
	// it always does.
	hostname := hostnameDoc(t, patch)
	assert.Equal(t, "HostnameConfig", hostname["kind"])
	assert.Equal(t, "platform-hel-control-plane-0", hostname["hostname"])
	assert.Equal(t, "off", hostname["auto"],
		`"off" is the only spelling Talos accepts for disabling the automatic hostname`)
}

func TestBuildNodePatch_SignsSANsIntoTheAPIServerToo(t *testing.T) {
	t.Parallel()

	// Talos signs the cluster endpoint into the apiserver certificate itself,
	// but not the node addresses. Setting only machine.certSANs makes the
	// Talos API answer everywhere while kube-apiserver answers on one name —
	// which looks fine on a single-node cluster and fails the moment kubectl
	// aims at a node behind a load balancer.
	patch, err := clusterspec.BuildNodePatch(clusterspec.NodePatchArgs{
		Hostname: "cp-0",
		CertSANs: []string{"203.0.113.10", "10.0.1.2"},
	})
	require.NoError(t, err)

	doc := decode(t, patch)
	machine, _ := doc["machine"].(map[string]any)
	cluster, _ := doc["cluster"].(map[string]any)
	apiServer, _ := cluster["apiServer"].(map[string]any)

	assert.Equal(t, machine["certSANs"], apiServer["certSANs"])
}

func TestBuildNodePatch_DedupesSANs(t *testing.T) {
	t.Parallel()

	// On a single control plane the node address and the cluster endpoint are
	// the same string; a duplicate is accepted but makes the certificate
	// harder to read.
	patch, err := clusterspec.BuildNodePatch(clusterspec.NodePatchArgs{
		Hostname: "cp-0",
		CertSANs: []string{"10.0.1.2", "10.0.1.2", "", "203.0.113.10"},
	})
	require.NoError(t, err)

	doc := decode(t, patch)
	machine, _ := doc["machine"].(map[string]any)

	assert.Equal(t, []any{"10.0.1.2", "203.0.113.10"}, machine["certSANs"])
}

func TestBuildNodePatch_LabelsAndTaints(t *testing.T) {
	t.Parallel()

	patch, err := clusterspec.BuildNodePatch(clusterspec.NodePatchArgs{
		Hostname:   "gpu-0",
		CertSANs:   []string{"10.0.1.80"},
		NodeLabels: map[string]string{"pool": "gpu"},
		NodeTaints: []string{"gpu=true:NoSchedule"},
	})
	require.NoError(t, err)

	doc := decode(t, patch)
	machine, _ := doc["machine"].(map[string]any)
	kubelet, _ := machine["kubelet"].(map[string]any)

	labels, _ := kubelet["nodeLabels"].(map[string]any)
	assert.Equal(t, "gpu", labels["pool"])

	taints, _ := kubelet["nodeTaints"].(map[string]any)
	assert.Equal(t, "true:NoSchedule", taints["gpu"])
}

func TestBuildNodePatch_OmitsKubeletSectionWhenNothingToSay(t *testing.T) {
	t.Parallel()

	// An empty kubelet block would be a no-op patch key that still shows in
	// diffs; leaving it out keeps the rendered config to what was asked for.
	patch, err := clusterspec.BuildNodePatch(clusterspec.NodePatchArgs{
		Hostname: "cp-0",
		CertSANs: []string{"10.0.1.2"},
	})
	require.NoError(t, err)

	doc := decode(t, patch)
	machine, _ := doc["machine"].(map[string]any)

	assert.NotContains(t, machine, "kubelet")
}

func TestBuildNodePatch_Rejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    clusterspec.NodePatchArgs
		wantMsg string
	}{
		{
			name:    "no hostname",
			args:    clusterspec.NodePatchArgs{CertSANs: []string{"10.0.1.2"}},
			wantMsg: "hostname is required",
		},
		{
			name:    "no certificate SANs",
			args:    clusterspec.NodePatchArgs{Hostname: "cp-0"},
			wantMsg: "at least one certificate SAN is required",
		},
		{
			name: "malformed taint",
			args: clusterspec.NodePatchArgs{
				Hostname: "cp-0", CertSANs: []string{"10.0.1.2"},
				NodeTaints: []string{"nonsense"},
			},
			wantMsg: "must be key=value:Effect",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := clusterspec.BuildNodePatch(tc.args)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantMsg)
		})
	}
}

func TestBuildNodePatch_HostileValuesCannotBreakTheDocument(t *testing.T) {
	t.Parallel()

	// The reason these patches are marshalled from structs rather than
	// rendered from a template: a value containing YAML syntax must end up as
	// a string, not as structure.
	patch, err := clusterspec.BuildNodePatch(clusterspec.NodePatchArgs{
		Hostname: "evil\nmachine:\n  install:\n    disk: /dev/sda",
		CertSANs: []string{"10.0.1.2"},
	})
	require.NoError(t, err)

	hostname := hostnameDoc(t, patch)

	assert.Equal(t, "evil\nmachine:\n  install:\n    disk: /dev/sda", hostname["hostname"])
	assert.NotContains(t, hostname, "install")
}

func TestBuildClusterPatch_DoesNotPassCloudProviderToTheAPIServer(t *testing.T) {
	t.Parallel()

	// Kubernetes removed --cloud-provider from kube-apiserver, so passing it
	// is fatal rather than redundant: the static pod exits with "unknown flag"
	// on every restart, the scheduler fails behind it unable to reach the API
	// through KubePrism, and the cluster settles with etcd and kubelet healthy
	// and 6443 refusing connections — which reads like a firewall problem.
	//
	// Nothing offline catches this. talosctl validates the shape of the
	// config, not whether a flag exists in the Kubernetes version it pins.
	raw, err := clusterspec.BuildClusterPatch(clusterspec.ClusterPatchArgs{
		PodCIDR:     "10.244.0.0/16",
		ServiceCIDR: "10.96.0.0/12",
		NodeSubnet:  "10.0.1.0/24",
		IPRange:     "10.0.0.0/16",
	})
	require.NoError(t, err)

	patch := decode(t, raw)

	cluster, ok := patch["cluster"].(map[string]any)
	require.True(t, ok)

	// The section exists now, because it carries the audit policy — so this
	// asserts what it is actually about rather than the section's absence.
	// --cloud-provider can only arrive through extraArgs, and the API server
	// needs none of those at all.
	apiServer, ok := cluster["apiServer"].(map[string]any)
	require.True(t, ok, "no apiServer section, so the audit policy is not being set either")

	extraArgs, present := apiServer["extraArgs"]
	assert.False(t, present,
		"the API server needs no extraArgs at all; it grew a cloud-provider flag once and that cost a bring-up: %v",
		extraArgs)

	// The two that do take it must keep it: the CCM clears the uninitialized
	// taint and programmes pod routes, and neither happens without this.
	kubelet, ok := patch["machine"].(map[string]any)["kubelet"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "external",
		kubelet["extraArgs"].(map[string]any)["cloud-provider"])

	controllerManager, ok := cluster["controllerManager"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "external",
		controllerManager["extraArgs"].(map[string]any)["cloud-provider"])
}

// volumeConfigs decodes the VolumeConfig documents of a cluster patch, keyed
// by the volume they name.
func volumeConfigs(t *testing.T, patch string) map[string]map[string]any {
	t.Helper()

	out := map[string]map[string]any{}

	for _, document := range documents(t, patch) {
		var parsed map[string]any
		require.NoError(t, yaml.Unmarshal([]byte(document), &parsed))

		if parsed["kind"] != "VolumeConfig" {
			continue
		}

		name, ok := parsed["name"].(string)
		require.True(t, ok, "a VolumeConfig with no name encrypts nothing")

		out[name] = parsed
	}

	return out
}

func TestBuildClusterPatch_EncryptsBothSystemVolumes(t *testing.T) {
	t.Parallel()

	// STATE holds the machine config and the node's secrets; EPHEMERAL holds
	// /var, which is etcd's data directory. Encrypting one and not the other
	// is a cluster whose secrets are still readable from a snapshot, so both
	// are named here rather than trusted to a loop somebody may shorten.
	patch, err := clusterspec.BuildClusterPatch(clusterspec.ClusterPatchArgs{
		PodCIDR:     "10.244.0.0/16",
		ServiceCIDR: "10.96.0.0/12",
		NodeSubnet:  "10.0.1.0/24",
		IPRange:     "10.0.0.0/16",
	})
	require.NoError(t, err)

	found := volumeConfigs(t, patch)

	for _, volume := range []string{clusterspec.VolumeSTATE, clusterspec.VolumeEPHEMERAL} {
		document, ok := found[volume]
		require.True(t, ok, "no VolumeConfig for %s", volume)

		assert.Equal(t, "v1alpha1", document["apiVersion"], volume)

		encryption, ok := document["encryption"].(map[string]any)
		require.True(t, ok, "%s has no encryption stanza", volume)
		assert.Equal(t, clusterspec.EncryptionProvider, encryption["provider"], volume)

		keys, ok := encryption["keys"].([]any)
		require.True(t, ok, "%s has no keys", volume)
		require.Len(t, keys, 1, "%s: one key, one slot", volume)

		key, ok := keys[0].(map[string]any)
		require.True(t, ok)

		// nodeID derives the key from the node's UUID, which is what makes a
		// restored snapshot unreadable. A `static` key would put the
		// passphrase in the machine config beside the data it protects.
		assert.Contains(t, key, "nodeID", volume)
		assert.NotContains(t, key, "static", volume)
		assert.Equal(t, float64(clusterspec.EncryptionKeySlot), key["slot"], volume)
	}

	assert.Len(t, found, 2, "only the two system volumes are configured here")
}

func TestBuildClusterPatch_DoesNotMixTheLegacyEncryptionForm(t *testing.T) {
	t.Parallel()

	// `machine.systemDiskEncryption` is the v1alpha1 spelling of the same
	// setting. Talos v1.13 documents VolumeConfig instead, and carrying both
	// for one volume is a conflict rather than a harmless duplicate — so the
	// machine config document must stay silent about encryption.
	patch, err := clusterspec.BuildClusterPatch(clusterspec.ClusterPatchArgs{
		PodCIDR:     "10.244.0.0/16",
		ServiceCIDR: "10.96.0.0/12",
		NodeSubnet:  "10.0.1.0/24",
		IPRange:     "10.0.0.0/16",
	})
	require.NoError(t, err)

	machine, ok := decode(t, patch)["machine"].(map[string]any)
	require.True(t, ok)

	assert.NotContains(t, machine, "systemDiskEncryption")
}

func TestBuildEtcdPatch_PinsPeersToThePrivateNetwork(t *testing.T) {
	t.Parallel()

	const nodeSubnet = "10.0.1.0/24"

	patch, err := clusterspec.BuildEtcdPatch(nodeSubnet)
	require.NoError(t, err)

	cluster, ok := decode(t, patch)["cluster"].(map[string]any)
	require.True(t, ok)

	etcd, ok := cluster["etcd"].(map[string]any)
	require.True(t, ok, "the patch carries no etcd block")

	// Without this etcd advertises whichever address comes first, which on
	// Hetzner is the public one — and the perimeter firewall opens tcp/6443
	// and tcp/50000 and nothing else, so members cannot reach each other's
	// tcp/2380. Measured on the first real three-member cluster: two members,
	// one of them a learner for ever, the third never joining.
	assert.Equal(t, []any{nodeSubnet}, etcd["advertisedSubnets"],
		"etcd must advertise inside the node subnet, not on the public address")
}

func TestBuildEtcdPatch_RequiresASubnet(t *testing.T) {
	t.Parallel()

	// Empty would produce a document Talos accepts and that pins nothing.
	_, err := clusterspec.BuildEtcdPatch("")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "nodeSubnet is required")
}

func TestBuildClusterPatch_CarriesNoEtcdSection(t *testing.T) {
	t.Parallel()

	// The shared patch goes to workers too, and Talos refuses the section
	// there: `etcd config is only allowed on control plane machines`. Found by
	// cluster:machine-config:check, which is why etcd has a patch of its own.
	patch, err := clusterspec.BuildClusterPatch(clusterspec.ClusterPatchArgs{
		PodCIDR:     "10.244.0.0/16",
		ServiceCIDR: "10.96.0.0/12",
		NodeSubnet:  "10.0.1.0/24",
		IPRange:     "10.0.0.0/16",
	})
	require.NoError(t, err)

	cluster, ok := decode(t, patch)["cluster"].(map[string]any)
	require.True(t, ok)

	assert.NotContains(t, cluster, "etcd",
		"the patch every role shares must not carry a control-plane-only section")
}

func TestNetworkGateway_IsTheFirstAddressOfTheRange(t *testing.T) {
	t.Parallel()

	// Derived rather than the literal "10.0.0.1", which is right only while
	// network.ipRange keeps its default. A topology that moved the range would
	// have got a route pointing into a network its nodes are not on.
	for ipRange, want := range map[string]string{
		"10.0.0.0/16":    "10.0.0.1",
		"10.1.0.0/16":    "10.1.0.1",
		"172.16.0.0/12":  "172.16.0.1",
		"192.168.0.0/24": "192.168.0.1",
		// Masked first: /16 written from a host address still means the
		// network's gateway, not the next address after that host.
		"10.0.5.7/16": "10.0.0.1",
	} {
		got, err := clusterspec.NetworkGateway(ipRange)
		require.NoError(t, err, ipRange)
		assert.Equal(t, want, got, ipRange)
	}
}

func TestNetworkGateway_RefusesWhatItCannotDerive(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{"", "10.0.0.0", "not-a-cidr", "10.0.0.0/33"} {
		_, err := clusterspec.NetworkGateway(bad)
		require.Error(t, err, bad)
	}
}

func TestBuildClusterPatch_RoutesThePodNetworkThroughThePrivateGateway(t *testing.T) {
	t.Parallel()

	// The route this platform could not work without, and did not have. A node
	// has no route for another node's pod CIDR — eth1 is a /32 whose only
	// on-link peer is the gateway — so without this a pod packet for another
	// node matches the DEFAULT route and leaves through the public interface.
	patch, err := clusterspec.BuildClusterPatch(clusterspec.ClusterPatchArgs{
		PodCIDR:     "10.244.0.0/16",
		ServiceCIDR: "10.96.0.0/12",
		NodeSubnet:  "10.0.1.0/24",
		IPRange:     "10.0.0.0/16",
	})
	require.NoError(t, err)

	machine, _ := decode(t, patch)["machine"].(map[string]any)
	network, _ := machine["network"].(map[string]any)

	list, ok := network["interfaces"].([]any)
	require.True(t, ok, "machine.network.interfaces is not a list")
	require.Len(t, list, 1)

	iface, ok := list[0].(map[string]any)
	require.True(t, ok)

	assert.Equal(t, clusterspec.PrivateInterface, iface["interface"])

	// DHCP stays on. Hetzner serves the private address over it, and declaring
	// the interface without this turns it off — taking the private address,
	// and every path that depends on it, with it.
	assert.Equal(t, true, iface["dhcp"], "declaring the interface must not disable DHCP")

	routes, ok := iface["routes"].([]any)
	require.True(t, ok, "no routes on the private interface")
	require.Len(t, routes, 1)

	route, ok := routes[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "10.244.0.0/16", route["network"], "the route must cover the pod network")
	assert.Equal(t, "10.0.0.1", route["gateway"], "the route must point at the private network's gateway")
}

func TestBuildClusterPatch_DerivesTheGatewayFromTheTopologysRange(t *testing.T) {
	t.Parallel()

	// A non-default range has to move the gateway with it. Pinned separately
	// from the case above so a hardcoded 10.0.0.1 fails here rather than
	// passing everywhere the default happens to be used.
	patch, err := clusterspec.BuildClusterPatch(clusterspec.ClusterPatchArgs{
		PodCIDR:     "10.244.0.0/16",
		ServiceCIDR: "10.96.0.0/12",
		NodeSubnet:  "172.16.1.0/24",
		IPRange:     "172.16.0.0/12",
	})
	require.NoError(t, err)

	machine, _ := decode(t, patch)["machine"].(map[string]any)
	network, _ := machine["network"].(map[string]any)
	list, _ := network["interfaces"].([]any)
	require.Len(t, list, 1)

	iface, _ := list[0].(map[string]any)
	routes, _ := iface["routes"].([]any)
	require.Len(t, routes, 1)

	route, _ := routes[0].(map[string]any)
	assert.Equal(t, "172.16.0.1", route["gateway"])
}

func TestBuildClusterPatch_RefusesATopologyWithNoRange(t *testing.T) {
	t.Parallel()

	// Refused rather than defaulted. A patch with no route is a cluster whose
	// pods cannot reach each other across nodes, and that failure is three
	// layers away from anything that names a network.
	_, err := clusterspec.BuildClusterPatch(clusterspec.ClusterPatchArgs{
		PodCIDR:     "10.244.0.0/16",
		ServiceCIDR: "10.96.0.0/12",
		NodeSubnet:  "10.0.1.0/24",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ipRange")
}

// TestClusterPatch_KubePrismPortIsTheOneCiliumIsPointedAt holds the two halves
// of a contract that had none.
//
// Talos listens on this port because this patch enables it; Cilium is pointed
// at it by layers/10-node-platform. A mismatch fails no apply — Cilium comes
// up pointing at a port nothing listens on, so there is no service dataplane
// and every ClusterIP blackholes with nothing saying why.
//
// It was 7445 twice: a constant in internal/pkg/chartsettings and a bare
// literal here, with a comment in the layer claiming they were one value.
func TestClusterPatch_KubePrismPortIsTheOneCiliumIsPointedAt(t *testing.T) {
	t.Parallel()

	patch, err := clusterspec.BuildClusterPatch(clusterspec.ClusterPatchArgs{
		PodCIDR:     "10.244.0.0/16",
		ServiceCIDR: "10.96.0.0/12",
		NodeSubnet:  "10.0.1.0/24",
		IPRange:     "10.0.0.0/16",
	})
	require.NoError(t, err)

	doc := decode(t, patch)

	machine, ok := doc["machine"].(map[string]any)
	require.True(t, ok, "the patch has no machine section")

	features, ok := machine["features"].(map[string]any)
	require.True(t, ok, "the patch enables no features")

	prism, ok := features["kubePrism"].(map[string]any)
	require.True(t, ok, "the patch does not enable KubePrism, which the CNI reaches the API through")

	assert.Equal(t, true, prism["enabled"])

	// float64, because YAML numbers decode as float64 through map[string]any.
	assert.Equal(t, float64(clusterspec.KubePrismPort), prism["port"],
		"Talos would listen on %v while Cilium is pointed at %d",
		prism["port"], clusterspec.KubePrismPort)
}
