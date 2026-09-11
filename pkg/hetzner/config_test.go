package hetzner_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/hetzner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validYAML is the smallest file that passes: everything else in these tests
// is this document with one thing changed, so a failure names the change.
const validYAML = `
apiVersion: hetzner-iac/v1
kind: Cluster
metadata:
  name: platform-hel
placement:
  location: hel1
  networkZone: eu-central
network:
  adminCIDRs:
    - 203.0.113.4/32
talos:
  version: v1.14.0
controlPlane:
  count: 3
  serverType: cx23
  apiLoadBalancerType: lb11
workerPools:
  - name: worker
    count: 2
    serverType: cx33
`

func TestParseTopology_Valid(t *testing.T) {
	t.Parallel()

	topology, err := hetzner.ParseTopology([]byte(validYAML))
	require.NoError(t, err)

	assert.Equal(t, "platform-hel", topology.Metadata.Name)
	assert.Equal(t, "hel1", topology.Placement.Location)
	assert.Equal(t, 3, topology.ControlPlane.Count)
	require.Len(t, topology.WorkerPools, 1)
	assert.Equal(t, "worker", topology.WorkerPools[0].Name)
}

func TestParseTopology_AppliesDefaults(t *testing.T) {
	t.Parallel()

	minimal := `
apiVersion: hetzner-iac/v1
kind: Cluster
metadata: { name: c }
placement: { location: fsn1 }
network:
  adminCIDRs: [203.0.113.4/32]
talos: { version: v1.14.0 }
`

	topology, err := hetzner.ParseTopology([]byte(minimal))
	require.NoError(t, err)

	// The network zone is derived rather than restated, so it cannot be
	// written inconsistently with the location.
	assert.Equal(t, "eu-central", topology.Placement.NetworkZone)
	assert.Equal(t, hetzner.DefaultIPRange, topology.Network.IPRange)
	assert.Equal(t, hetzner.DefaultNodeSubnet, topology.Network.NodeSubnet)
	assert.Equal(t, hetzner.DefaultPodCIDR, topology.Network.PodCIDR)
	assert.Equal(t, hetzner.DefaultServiceCIDR, topology.Network.ServiceCIDR)
	assert.Equal(t, hetzner.DefaultArchitecture, topology.Talos.Architecture)
	assert.Equal(t, hetzner.DefaultCPServerType, topology.ControlPlane.ServerType)
	assert.Equal(t, 1, topology.ControlPlane.Count)

	// A single control plane gets no load balancer: there is nothing to fail
	// over between, so defaulting one in would only cost money.
	assert.Empty(t, topology.ControlPlane.APILoadBalancerType)
}

func TestApplyDefaults_LoadBalancerOnlyForHA(t *testing.T) {
	t.Parallel()

	ha := hetzner.Topology{ControlPlane: hetzner.ControlPlaneSpec{Count: 3}}
	ha.ApplyDefaults()
	assert.Equal(t, hetzner.DefaultAPILBType, ha.ControlPlane.APILoadBalancerType)

	single := hetzner.Topology{ControlPlane: hetzner.ControlPlaneSpec{Count: 1}}
	single.ApplyDefaults()
	assert.Empty(t, single.ControlPlane.APILoadBalancerType)
}

func TestParseTopology_RejectsUnknownField(t *testing.T) {
	t.Parallel()

	// A key nobody reads should stop the run rather than be ignored: in a file
	// that describes a cluster, an ignored key is a setting the author believes
	// is in effect and is not.
	withStrayKey := validYAML + "\nworkerPoolz:\n  - name: typo\n"

	_, err := hetzner.ParseTopology([]byte(withStrayKey))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown field "workerPoolz"`)
}

func TestParseTopology_FieldMatchingIsCaseInsensitive(t *testing.T) {
	t.Parallel()

	// Pinning a property of encoding/json that is easy to assume away: field
	// matching ignores case, so a lowercase key is NOT an unknown field and
	// still populates the struct. Worth a test because the natural assumption
	// — that strict decoding catches every spelling mistake — is wrong, and
	// acting on it would mean trusting the parser to catch typos it cannot.
	lowercased := strings.Replace(validYAML, "workerPools:", "workerpools:", 1)

	topology, err := hetzner.ParseTopology([]byte(lowercased))
	require.NoError(t, err)
	require.Len(t, topology.WorkerPools, 1)
	assert.Equal(t, "worker", topology.WorkerPools[0].Name)
}

func TestValidate_Rejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*hetzner.Topology)
		wantMsg string
	}{
		{
			name:    "empty admin CIDRs",
			mutate:  func(top *hetzner.Topology) { top.Network.AdminCIDRs = nil },
			wantMsg: "refusing to build a cluster whose Kubernetes and Talos APIs are open",
		},
		{
			name:    "admin CIDR is the whole internet",
			mutate:  func(top *hetzner.Topology) { top.Network.AdminCIDRs = []string{"0.0.0.0/0"} },
			wantMsg: "which is the whole internet",
		},
		{
			name:    "admin CIDR is not a CIDR",
			mutate:  func(top *hetzner.Topology) { top.Network.AdminCIDRs = []string{"203.0.113.4"} },
			wantMsg: "is not a CIDR",
		},
		{
			name:    "even control plane count",
			mutate:  func(top *hetzner.Topology) { top.ControlPlane.Count = 2 },
			wantMsg: "an even count costs more without tolerating more failures",
		},
		{
			name:    "zero control plane count",
			mutate:  func(top *hetzner.Topology) { top.ControlPlane.Count = -1 },
			wantMsg: "must be at least 1",
		},
		{
			name: "HA without a load balancer",
			mutate: func(top *hetzner.Topology) {
				top.ControlPlane.Count = 3
				top.ControlPlane.APILoadBalancerType = ""
			},
			wantMsg: "apiLoadBalancerType is required when controlPlane.count > 1",
		},
		{
			name:    "unknown location",
			mutate:  func(top *hetzner.Topology) { top.Placement.Location = "lon1" },
			wantMsg: "is not an hcloud location",
		},
		{
			name:    "zone does not contain location",
			mutate:  func(top *hetzner.Topology) { top.Placement.NetworkZone = "us-east" },
			wantMsg: "does not contain location",
		},
		{
			name:    "pod CIDR overlaps the private network",
			mutate:  func(top *hetzner.Topology) { top.Network.PodCIDR = "10.0.128.0/17" },
			wantMsg: "overlaps network.ipRange",
		},
		{
			name:    "node subnet outside the private network",
			mutate:  func(top *hetzner.Topology) { top.Network.NodeSubnet = "192.168.5.0/24" },
			wantMsg: "is not inside network.ipRange",
		},
		{
			name:    "CIDR with host bits set",
			mutate:  func(top *hetzner.Topology) { top.Network.NodeSubnet = "10.0.1.5/24" },
			wantMsg: "has host bits set, did you mean 10.0.1.0/24?",
		},
		{
			name:    "malformed Talos version",
			mutate:  func(top *hetzner.Topology) { top.Talos.Version = "1.14.0" },
			wantMsg: "must look like v1.14.0",
		},
		{
			name:    "unknown architecture",
			mutate:  func(top *hetzner.Topology) { top.Talos.Architecture = "riscv" },
			wantMsg: "must be x86 or arm",
		},
		{
			name:    "cluster name is not DNS-1123",
			mutate:  func(top *hetzner.Topology) { top.Metadata.Name = "Platform_HEL" },
			wantMsg: "must be lowercase alphanumeric",
		},
		{
			name:    "cluster name leaves no room for node suffixes",
			mutate:  func(top *hetzner.Topology) { top.Metadata.Name = strings.Repeat("a", 48) },
			wantMsg: "longer than 47 characters",
		},
		{
			name: "duplicate worker pool names",
			mutate: func(top *hetzner.Topology) {
				top.WorkerPools = append(top.WorkerPools, hetzner.WorkerPoolSpec{
					Name: "worker", Count: 1, ServerType: "cx33",
				})
			},
			wantMsg: `duplicates workerPools[0]`,
		},
		{
			name: "worker pool larger than its address slice",
			mutate: func(top *hetzner.Topology) {
				top.WorkerPools[0].Count = hetzner.PoolAddressStride + 1
			},
			wantMsg: "more than the 40 addresses a pool owns",
		},
		{
			name:    "malformed taint",
			mutate:  func(top *hetzner.Topology) { top.WorkerPools[0].Taints = []string{"gpu=true:Nope"} },
			wantMsg: "must be key=value:Effect",
		},
		{
			name:    "wrong apiVersion",
			mutate:  func(top *hetzner.Topology) { top.APIVersion = "caryon/v1" },
			wantMsg: `apiVersion must be "hetzner-iac/v1"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			topology := mustParse(t, validYAML)
			tc.mutate(topology)

			err := topology.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantMsg)
		})
	}
}

func TestValidate_ReportsEveryProblemAtOnce(t *testing.T) {
	t.Parallel()

	// A topology change is reviewed once and applied once; one error per run
	// would turn this file into three round trips.
	topology := mustParse(t, validYAML)
	topology.Network.AdminCIDRs = nil
	topology.ControlPlane.Count = 2
	topology.Talos.Architecture = "riscv"

	err := topology.Validate()
	require.Error(t, err)

	assert.Contains(t, err.Error(), "adminCIDRs")
	assert.Contains(t, err.Error(), "even count")
	assert.Contains(t, err.Error(), "x86 or arm")
}

func TestValidate_PoolCapacityAgainstSubnet(t *testing.T) {
	t.Parallel()

	topology := mustParse(t, validYAML)

	// /26 holds 64 addresses: one 40-address slice for the control plane
	// leaves room for no pool at all.
	topology.Network.IPRange = "10.0.0.0/16"
	topology.Network.NodeSubnet = "10.0.1.0/26"

	err := topology.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "holds 64")
}

func TestValidate_AcceptsZeroWorkerPools(t *testing.T) {
	t.Parallel()

	// A control-plane-only cluster is a legitimate shape — workers can be
	// added later without recreating anything.
	topology := mustParse(t, validYAML)
	topology.WorkerPools = nil

	require.NoError(t, topology.Validate())
}

func mustParse(t *testing.T, raw string) *hetzner.Topology {
	t.Helper()

	topology, err := hetzner.ParseTopology([]byte(raw))
	require.NoError(t, err)

	return topology
}

func TestApplyDefaults_PinsTheKubernetesVersion(t *testing.T) {
	t.Parallel()

	// An empty version used to mean "whatever the configured Talos ships",
	// which lets a Talos patch bump move Kubernetes a whole minor with no diff
	// and no decision. That is how this cluster first came up on v1.36.0 —
	// new enough that kube-apiserver had removed a flag the machine config was
	// passing, so the control plane would not start at all.
	topology := &hetzner.Topology{}
	topology.ApplyDefaults()

	assert.Equal(t, hetzner.DefaultKubernetesVersion, topology.Kubernetes.Version)
	assert.NotEmpty(t, topology.Kubernetes.Version,
		"there must be no way to ask for whatever Talos ships")
}

func TestApplyDefaults_DoesNotOverrideAnExplicitKubernetesVersion(t *testing.T) {
	t.Parallel()

	topology := &hetzner.Topology{}
	topology.Kubernetes.Version = "v1.35.7"
	topology.ApplyDefaults()

	assert.Equal(t, "v1.35.7", topology.Kubernetes.Version)
}

func TestEveryTopologyPresentPinsKubernetes(t *testing.T) {
	t.Parallel()

	// The default in the package is a floor, not the promise. A topology
	// states its version, so an upgrade is a line in a diff someone reviews.
	//
	// Globbed: only cluster.example.yaml is committed, and a working copy also
	// has the stack files it was copied into.
	paths, err := filepath.Glob(filepath.Join("..", "..", "infra", "cluster", "cluster.*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, paths)

	for _, path := range paths {
		raw, readErr := os.ReadFile(path)
		require.NoError(t, readErr, path)

		assert.Contains(t, string(raw), "version: "+hetzner.DefaultKubernetesVersion,
			"%s should pin Kubernetes explicitly", path)
	}
}
