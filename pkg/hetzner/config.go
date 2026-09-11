// Package hetzner builds a Talos-based Kubernetes cluster on Hetzner Cloud:
// the private network, the public-interface firewall, the control plane and
// the worker pools.
//
// Hetzner has no managed Kubernetes, so this package builds the cluster
// rather than requesting one. It stops at "a Kubernetes API that answers":
// the CNI is not installed here, it belongs to the 10-cni layer, because a
// cluster and its CNI have different lifecycles and pinning them together
// makes a CNI upgrade a cluster change.
package hetzner

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strings"
)

// Topology is the committed description of a cluster: what
// infra/cluster/cluster.<stack>.yaml deserialises into.
//
// It is committed on purpose. A cluster must be reviewable in a diff before
// it exists and reproducible from a clone, which rules out parameters that
// live only in stack config somebody set by hand.
//
// Sparse by design: an omitted field keeps the default applied here, so the
// file never restates a default and a default can change in one place.
type Topology struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`

	Metadata     MetadataSpec     `json:"metadata"`
	Placement    PlacementSpec    `json:"placement"`
	Network      NetworkSpec      `json:"network"`
	Talos        TalosSpec        `json:"talos"`
	Kubernetes   KubernetesSpec   `json:"kubernetes"`
	ControlPlane ControlPlaneSpec `json:"controlPlane"`
	WorkerPools  []WorkerPoolSpec `json:"workerPools"`
}

// MetadataSpec names the cluster.
type MetadataSpec struct {
	// Name is the Talos cluster name, the server-name prefix and the value
	// of the label every resource in the cluster carries.
	Name string `json:"name"`
}

// PlacementSpec says where in Hetzner's estate the cluster lives.
type PlacementSpec struct {
	// Location is an hcloud location: fsn1, nbg1, hel1, ash, hil or sin.
	Location string `json:"location"`
	// NetworkZone must be the zone containing Location. Hetzner rejects the
	// mismatch at apply time; this is checked here so it fails at plan time.
	NetworkZone string `json:"networkZone"`
}

// NetworkSpec describes the private network, the address ranges inside it,
// and who may reach the cluster's control APIs.
type NetworkSpec struct {
	// IPRange is the private network range the cluster lives in.
	IPRange string `json:"ipRange"`
	// NodeSubnet is the subnet inside IPRange that nodes get addresses from.
	NodeSubnet string `json:"nodeSubnet"`
	// PodCIDR is the pod network. It must not overlap IPRange: the CCM route
	// controller programmes routes for it inside the private network, and an
	// overlap makes node and pod traffic ambiguous.
	PodCIDR string `json:"podCIDR"`
	// ServiceCIDR is the cluster service network.
	ServiceCIDR string `json:"serviceCIDR"`
	// AdminCIDRs are the only sources allowed to reach the Kubernetes API
	// (tcp/6443) and the Talos API (tcp/50000).
	//
	// It MUST contain the host running the apply: Talos machine configuration
	// is applied over the Talos API from there, and a host outside this list
	// hangs with the port filtered rather than failing cleanly.
	//
	// There is deliberately no default. An empty list is refused rather than
	// widened to 0.0.0.0/0 — a cluster whose control plane is open to the
	// internet should be something somebody typed, not something a default did.
	AdminCIDRs []string `json:"adminCIDRs"`

	// PublicIPv4 keeps a routable address on every node, and decides which
	// address Talos and the kubeconfig target. Defaults to true: machine
	// configuration is pushed over the Talos API, so an apply from anywhere
	// but inside the private network needs one.
	//
	// AllowICMP opens ping from AdminCIDRs. Defaults to false.
	//
	// Both are pointers because this file is sparse: an omitted switch is not
	// the same as a false one. PublicIPv4 defaults to TRUE, so a plain bool
	// would read an omitted field as false and silently strip every node's
	// public address — and with it the endpoint the kubeconfig points at.
	PublicIPv4 *bool `json:"publicIPv4,omitempty"`
	AllowICMP  *bool `json:"allowICMP,omitempty"`
}

// TalosSpec pins the Talos version contract and the architecture it was
// built for. Both must match the baked snapshot.
type TalosSpec struct {
	// Version is the Talos version contract. It must match the snapshot baked
	// into the project: the image lookup keys off this value, so bumping it
	// without re-baking finds no snapshot and fails at plan time.
	Version string `json:"version"`
	// Architecture is x86 or arm. arm selects the CAX server types.
	Architecture string `json:"architecture"`
	// ImageSelector overrides the label selector used to find the baked Talos
	// snapshot. Empty derives "os=talos,talos-version=<Version>", which is
	// what `task cluster:image-bake` writes.
	ImageSelector string `json:"imageSelector,omitempty"`
}

// KubernetesSpec pins the Kubernetes version, or leaves it to Talos.
type KubernetesSpec struct {
	// Version pins the Kubernetes version. Empty takes
	// DefaultKubernetesVersion, which is also pinned — there is no way to ask
	// for "whatever Talos ships", for the reason stated there.
	Version string `json:"version"`
}

// ControlPlaneSpec sizes the control plane and the load balancer in front of
// it.
type ControlPlaneSpec struct {
	// Count must be odd and positive: etcd needs a quorum, and an even count
	// costs more without tolerating more failures.
	Count int `json:"count"`
	// ServerType is the hcloud server type for control-plane nodes.
	ServerType string `json:"serverType"`
	// APILoadBalancerType fronts the kube-apiserver. Required once Count > 1
	// and ignored otherwise — a single control plane has nothing to fail over
	// between, so creating a load balancer would only cost money.
	APILoadBalancerType string `json:"apiLoadBalancerType"`
}

// WorkerPoolSpec is one group of identically shaped worker nodes. Node shape
// is a property of a pool, so a GPU pool and a general pool differ in server
// type, labels and taints while sharing one control plane.
type WorkerPoolSpec struct {
	Name       string `json:"name"`
	Count      int    `json:"count"`
	ServerType string `json:"serverType"`
	// Labels are applied as Kubernetes node labels on every node in the pool.
	Labels map[string]string `json:"labels,omitempty"`
	// Taints are applied as Kubernetes node taints, in the standard
	// key=value:Effect spelling.
	Taints []string `json:"taints,omitempty"`
}

// Defaults applied to any field the committed file leaves empty.
const (
	DefaultIPRange       = "10.0.0.0/16"
	DefaultNodeSubnet    = "10.0.1.0/24"
	DefaultPodCIDR       = "10.244.0.0/16"
	DefaultServiceCIDR   = "10.96.0.0/12"
	DefaultArchitecture  = "x86"
	DefaultCPServerType  = "cx23"
	DefaultAPILBType     = "lb11"
	DefaultWorkerSrvType = "cx33"

	// DefaultKubernetesVersion is pinned rather than left to Talos.
	//
	// An empty version used to mean "whatever the configured Talos ships",
	// which makes a Talos patch bump able to move Kubernetes a whole minor
	// without a decision or a diff. That is how this cluster ended up on
	// v1.36.0: new enough that kube-apiserver had already removed a flag the
	// machine config was passing, and the control plane would not start.
	//
	// v1.36 is the minor Talos v1.13.10 defaults to, so this is the supported
	// pairing; .4 is the newest patch in that line. Bumping Talos means
	// revisiting this line deliberately — which is the point.
	DefaultKubernetesVersion = "v1.36.4"

	// PoolAddressStride is how many addresses of the node subnet each worker
	// pool owns. Pools are placed at fixed, non-overlapping offsets so that
	// adding a pool never renumbers an existing one — renumbering would
	// replace every node in it.
	PoolAddressStride = 40

	// MaxServerName is hcloud's limit on a server name.
	MaxServerName = 63

	// NodeNameSuffix is the room NodeName needs beyond the cluster name.
	// It builds "<cluster>-<pool>-<ordinal>", and the longest pool name this
	// repository produces on its own is the control-plane role.
	//
	// Derived from RoleControlPlane rather than counted by hand: the first
	// attempt at this constant guessed the suffix, got 48 instead of 47, and
	// would have let a name through that hcloud then rejects.
	NodeNameSuffix = len("-") + len(RoleControlPlane) + len("-") + 1

	// MaxClusterNameLength is what remains for metadata.name. Derived rather
	// than written as 47, so a renamed role or a longer ordinal cannot
	// silently push node names past hcloud's limit — the arithmetic is the
	// documentation.
	MaxClusterNameLength = MaxServerName - NodeNameSuffix

	apiVersion = "hetzner-iac/v1"
	kind       = "Cluster"
)

// PodSecurityExemptNamespaces are the namespaces Talos leaves out of Pod
// Security Admission.
//
// Talos enables PSA by default with `enforce: baseline` for every namespace
// and exempts exactly one — read off a running node, not assumed:
//
//	defaults:   {enforce: baseline, audit: restricted, warn: restricted}
//	exemptions: {namespaces: [kube-system]}
//
// A workload that needs host namespaces, hostPath or a hostPort can therefore
// only run here. That is not a detail: it cost two failed deploys. The
// node-exporter DaemonSet had DESIRED 1 and CURRENT 0 — no pod was created at
// all — and the only evidence was one event on the DaemonSet saying
// "violates PodSecurity baseline:latest".
var PodSecurityExemptNamespaces = []string{"kube-system"}

// KubeProxyDisabled is what this repository writes into the Talos machine
// configuration, and therefore a constraint on which CNI can be installed: a
// CNI that does not replace kube-proxy would leave the cluster with no service
// dataplane at all.
//
// A named constant rather than a literal in machineconfig.go because pkg/cni
// has to be able to check against it — two halves of one decision, in one
// place, which is the same reason pkg/platform exists.
const KubeProxyDisabled = true

// Defaults for the two switches. Separate from the block above because a bool
// default cannot be expressed as "the zero value is fine": DefaultPublicIPv4
// is true, which is the whole reason those fields are pointers.
var (
	DefaultPublicIPv4 = true
	DefaultAllowICMP  = false
)

// validLocations maps each hcloud location to the network zone that contains
// it. Hetzner rejects a location/zone mismatch when the subnet is created,
// which is late; this map moves that failure to plan time.
var validLocations = map[string]string{
	"fsn1": "eu-central",
	"nbg1": "eu-central",
	"hel1": "eu-central",
	"ash":  "us-east",
	"hil":  "us-west",
	"sin":  "ap-southeast",
}

var (
	// dns1123 is the Kubernetes label/name shape. The cluster name becomes a
	// server-name prefix and an hcloud label value, both of which are stricter
	// than a free string.
	dns1123 = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	// semverish accepts the vX.Y.Z spelling Talos and Kubernetes both use.
	semverish = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)
	// taintPattern is key=value:Effect. Value may be empty (key=:NoSchedule).
	taintPattern = regexp.MustCompile(`^[^=:]+=[^:]*:(NoSchedule|PreferNoSchedule|NoExecute)$`)
)

// ErrEmptyAdminCIDRs is returned when the admin CIDR list is missing. It is a
// named error because it is the one validation failure with a security
// consequence rather than a correctness one, and callers surface it specially.
var ErrEmptyAdminCIDRs = errors.New("network.adminCIDRs is empty: refusing to build a cluster whose Kubernetes and Talos APIs are open to the internet")

// ApplyDefaults fills in every field that has a safe default. Fields with no
// safe default — the cluster name, the admin CIDRs — are left empty for
// Validate to reject.
func (t *Topology) ApplyDefaults() {
	if t.APIVersion == "" {
		t.APIVersion = apiVersion
	}

	if t.Kind == "" {
		t.Kind = kind
	}

	if t.Placement.Location != "" && t.Placement.NetworkZone == "" {
		// Derivable with certainty from the location, so asking for it twice
		// only creates a way to write them inconsistently.
		t.Placement.NetworkZone = validLocations[t.Placement.Location]
	}

	setIfEmpty(&t.Network.IPRange, DefaultIPRange)
	setIfEmpty(&t.Network.NodeSubnet, DefaultNodeSubnet)
	setIfEmpty(&t.Network.PodCIDR, DefaultPodCIDR)
	setIfEmpty(&t.Network.ServiceCIDR, DefaultServiceCIDR)
	setIfEmpty(&t.Talos.Architecture, DefaultArchitecture)
	setIfEmpty(&t.Kubernetes.Version, DefaultKubernetesVersion)
	setIfEmpty(&t.ControlPlane.ServerType, DefaultCPServerType)

	if t.ControlPlane.Count == 0 {
		t.ControlPlane.Count = 1
	}

	if t.ControlPlane.Count > 1 {
		setIfEmpty(&t.ControlPlane.APILoadBalancerType, DefaultAPILBType)
	}

	for i := range t.WorkerPools {
		setIfEmpty(&t.WorkerPools[i].ServerType, DefaultWorkerSrvType)
	}

	setBoolIfUnset(&t.Network.PublicIPv4, DefaultPublicIPv4)
	setBoolIfUnset(&t.Network.AllowICMP, DefaultAllowICMP)
}

// PublicIPv4Enabled reports whether nodes keep a routable address. Safe on a
// Topology that never went through ApplyDefaults, where the pointer is nil and
// the default — true — applies.
func (t *Topology) PublicIPv4Enabled() bool {
	return t.Network.PublicIPv4 == nil || *t.Network.PublicIPv4
}

// ICMPAllowed reports whether ping is open from the admin CIDRs.
func (t *Topology) ICMPAllowed() bool {
	return t.Network.AllowICMP != nil && *t.Network.AllowICMP
}

func setIfEmpty(field *string, value string) {
	if *field == "" {
		*field = value
	}
}

func setBoolIfUnset(field **bool, value bool) {
	if *field == nil {
		*field = &value
	}
}

// Validate reports every problem it finds, not just the first. A topology
// change is usually reviewed once and applied once; returning one error per
// run turns a five-mistake file into five round trips.
func (t *Topology) Validate() error {
	var problems []string

	problems = append(problems, t.validateIdentity()...)
	problems = append(problems, t.validatePlacement()...)
	problems = append(problems, t.validateNetwork()...)
	problems = append(problems, t.validateVersions()...)
	problems = append(problems, t.validateControlPlane()...)
	problems = append(problems, t.validateWorkerPools()...)

	if len(problems) == 0 {
		return nil
	}

	return fmt.Errorf("invalid cluster topology:\n  - %s", strings.Join(problems, "\n  - "))
}

func (t *Topology) validateIdentity() []string {
	var problems []string

	if t.APIVersion != apiVersion {
		problems = append(problems, fmt.Sprintf("apiVersion must be %q, got %q", apiVersion, t.APIVersion))
	}

	if t.Kind != kind {
		problems = append(problems, fmt.Sprintf("kind must be %q, got %q", kind, t.Kind))
	}

	switch {
	case t.Metadata.Name == "":
		problems = append(problems, "metadata.name is required")
	case len(t.Metadata.Name) > MaxClusterNameLength:
		problems = append(problems, fmt.Sprintf(
			"metadata.name %q is longer than %d characters, leaving no room for node-name suffixes "+
				"within hcloud's %d-character server-name limit",
			t.Metadata.Name, MaxClusterNameLength, MaxServerName))
	case !dns1123.MatchString(t.Metadata.Name):
		problems = append(problems, fmt.Sprintf("metadata.name %q must be lowercase alphanumeric with internal hyphens", t.Metadata.Name))
	}

	return problems
}

func (t *Topology) validatePlacement() []string {
	var problems []string

	zone, known := validLocations[t.Placement.Location]
	if !known {
		problems = append(problems, fmt.Sprintf("placement.location %q is not an hcloud location (%s)",
			t.Placement.Location, strings.Join(sortedKeys(validLocations), ", ")))

		return problems
	}

	if t.Placement.NetworkZone != zone {
		problems = append(problems, fmt.Sprintf("placement.networkZone %q does not contain location %q (expected %q)",
			t.Placement.NetworkZone, t.Placement.Location, zone))
	}

	return problems
}

func (t *Topology) validateNetwork() []string {
	var problems []string

	ipRange, ok := parsePrefix("network.ipRange", t.Network.IPRange, &problems)
	nodeSubnet, subnetOK := parsePrefix("network.nodeSubnet", t.Network.NodeSubnet, &problems)
	podCIDR, podOK := parsePrefix("network.podCIDR", t.Network.PodCIDR, &problems)
	_, _ = parsePrefix("network.serviceCIDR", t.Network.ServiceCIDR, &problems)

	if ok && subnetOK && !ipRange.Overlaps(nodeSubnet) {
		problems = append(problems, fmt.Sprintf("network.nodeSubnet %s is not inside network.ipRange %s",
			t.Network.NodeSubnet, t.Network.IPRange))
	}

	if ok && podOK && ipRange.Overlaps(podCIDR) {
		// The CCM route controller programmes pod routes inside the private
		// network. Overlapping ranges make node and pod traffic ambiguous and
		// the symptom is intermittent, not immediate.
		problems = append(problems, fmt.Sprintf("network.podCIDR %s overlaps network.ipRange %s",
			t.Network.PodCIDR, t.Network.IPRange))
	}

	if len(t.Network.AdminCIDRs) == 0 {
		problems = append(problems, ErrEmptyAdminCIDRs.Error())
	}

	for i, cidr := range t.Network.AdminCIDRs {
		prefix, parsed := parsePrefix(fmt.Sprintf("network.adminCIDRs[%d]", i), cidr, &problems)
		if parsed && prefix.Bits() == 0 {
			problems = append(problems, fmt.Sprintf(
				"network.adminCIDRs[%d] is %s, which is the whole internet: name the operator networks explicitly", i, cidr))
		}
	}

	return problems
}

func (t *Topology) validateVersions() []string {
	var problems []string

	if !semverish.MatchString(t.Talos.Version) {
		problems = append(problems, fmt.Sprintf("talos.version %q must look like v1.14.0", t.Talos.Version))
	}

	if t.Kubernetes.Version != "" && !semverish.MatchString(t.Kubernetes.Version) {
		problems = append(problems, fmt.Sprintf(
			"kubernetes.version %q must look like v1.36.4, or be empty to take the pinned default",
			t.Kubernetes.Version))
	}

	if t.Talos.Architecture != "x86" && t.Talos.Architecture != "arm" {
		problems = append(problems, fmt.Sprintf("talos.architecture %q must be x86 or arm", t.Talos.Architecture))
	}

	return problems
}

func (t *Topology) validateControlPlane() []string {
	var problems []string

	switch {
	case t.ControlPlane.Count < 1:
		problems = append(problems, fmt.Sprintf("controlPlane.count is %d, must be at least 1", t.ControlPlane.Count))
	case t.ControlPlane.Count%2 == 0:
		problems = append(problems, fmt.Sprintf("controlPlane.count is %d: an even count costs more without tolerating more failures — use %d or %d",
			t.ControlPlane.Count, t.ControlPlane.Count-1, t.ControlPlane.Count+1))
	}

	if t.ControlPlane.Count > 1 && t.ControlPlane.APILoadBalancerType == "" {
		problems = append(problems, "controlPlane.apiLoadBalancerType is required when controlPlane.count > 1: without it the API has no stable address to fail over to")
	}

	if t.ControlPlane.ServerType == "" {
		problems = append(problems, "controlPlane.serverType is required")
	}

	return problems
}

func (t *Topology) validateWorkerPools() []string {
	var problems []string

	seen := make(map[string]int, len(t.WorkerPools))

	for i, pool := range t.WorkerPools {
		field := fmt.Sprintf("workerPools[%d]", i)

		switch {
		case pool.Name == "":
			problems = append(problems, field+".name is required")
		case !dns1123.MatchString(pool.Name):
			problems = append(problems, fmt.Sprintf("%s.name %q must be lowercase alphanumeric with internal hyphens", field, pool.Name))
		}

		if first, duplicate := seen[pool.Name]; duplicate && pool.Name != "" {
			// Pool name decides node names and the pool's address offset, so a
			// duplicate is two pools fighting over the same servers.
			problems = append(problems, fmt.Sprintf("%s.name %q duplicates workerPools[%d]", field, pool.Name, first))
		} else if pool.Name != "" {
			seen[pool.Name] = i
		}

		if pool.Count < 0 {
			problems = append(problems, fmt.Sprintf("%s.count is %d, must not be negative", field, pool.Count))
		}

		if pool.Count > PoolAddressStride {
			problems = append(problems, fmt.Sprintf("%s.count is %d, more than the %d addresses a pool owns in the node subnet",
				field, pool.Count, PoolAddressStride))
		}

		if pool.ServerType == "" {
			problems = append(problems, field+".serverType is required")
		}

		for j, taint := range pool.Taints {
			if !taintPattern.MatchString(taint) {
				problems = append(problems, fmt.Sprintf("%s.taints[%d] %q must be key=value:Effect where Effect is NoSchedule, PreferNoSchedule or NoExecute",
					field, j, taint))
			}
		}
	}

	problems = append(problems, t.validatePoolCapacity()...)

	return problems
}

// validatePoolCapacity checks that every pool's fixed address slice fits in
// the node subnet. Control-plane nodes take the first slice, so pool N starts
// at (N+1)*PoolAddressStride.
func (t *Topology) validatePoolCapacity() []string {
	prefix, err := netip.ParsePrefix(t.Network.NodeSubnet)
	if err != nil {
		return nil // already reported by validateNetwork
	}

	hostBits := prefix.Addr().BitLen() - prefix.Bits()
	if hostBits > 20 {
		return nil // large enough that the arithmetic below cannot overflow into a real limit
	}

	capacity := 1 << hostBits
	needed := (len(t.WorkerPools) + 1) * PoolAddressStride

	if needed > capacity {
		return []string{fmt.Sprintf(
			"%d worker pools need %d addresses (%d per pool, plus one slice for the control plane) but network.nodeSubnet %s holds %d",
			len(t.WorkerPools), needed, PoolAddressStride, t.Network.NodeSubnet, capacity)}
	}

	return nil
}

func parsePrefix(field, value string, problems *[]string) (netip.Prefix, bool) {
	if value == "" {
		*problems = append(*problems, field+" is required")

		return netip.Prefix{}, false
	}

	prefix, err := netip.ParsePrefix(value)
	if err != nil {
		*problems = append(*problems, fmt.Sprintf("%s %q is not a CIDR: %v", field, value, err))

		return netip.Prefix{}, false
	}

	if prefix.Addr() != prefix.Masked().Addr() {
		*problems = append(*problems, fmt.Sprintf("%s %q has host bits set, did you mean %s?", field, value, prefix.Masked()))

		return netip.Prefix{}, false
	}

	return prefix, true
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	return keys
}
