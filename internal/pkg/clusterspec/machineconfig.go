package clusterspec

import (
	"errors"
	"fmt"
	"net/netip"

	"sigs.k8s.io/yaml"
)

// Talos machine configuration is assembled from patches rather than written
// whole: `talosctl gen config` produces a valid baseline for the version
// contract, and these patches carry only what this cluster changes. Writing
// the whole document instead would mean re-deriving every default on a Talos
// upgrade.
//
// The patches are built as typed structs and marshalled, not rendered from
// string templates. A template that interpolates an address into YAML is one
// bad value away from producing a document that parses as something else; a
// struct cannot be broken that way, and it can be unit-tested without a
// cluster.

// Disk encryption, as VolumeConfig documents.
//
// STATE holds the machine config and the node's secrets and certificates;
// EPHEMERAL holds /var, which is etcd's data directory and every container's
// writable layer. Unencrypted, both are readable by anyone who can attach the
// volume or restore a snapshot of it — which on a cloud provider is a
// different and much cheaper attack than reaching the running node.
//
// Kubernetes Secrets are already encrypted inside etcd by the secretbox key
// the Talos secrets bundle generates, and that is a narrower thing than this:
// it covers the `secrets` resource and nothing else. ConfigMaps, CRD contents
// and the machine config itself are plaintext on the volume without this.
const (
	// VolumeSTATE and VolumeEPHEMERAL are the system volumes Talos names.
	// Spelled once: a VolumeConfig naming a volume that does not exist is
	// accepted and encrypts nothing.
	VolumeSTATE     = "STATE"
	VolumeEPHEMERAL = "EPHEMERAL"

	// EncryptionProvider is the only provider Talos offers for these.
	EncryptionProvider = "luks2"

	// EncryptionKeySlot is the LUKS slot the key goes in. One key, one slot.
	EncryptionKeySlot = 0
)

// ClusterPatchArgs describes the patch shared by every node in the cluster.
type ClusterPatchArgs struct {
	// PodCIDR and ServiceCIDR must match what the CNI is later configured
	// with. Talos writes them into the controller-manager and the apiserver,
	// so a mismatch surfaces as pods that never get routable addresses.
	PodCIDR     string
	ServiceCIDR string

	// NodeSubnet constrains which interface kubelet picks for the node IP.
	// Without it a node with a public address advertises that address, and
	// every intra-cluster connection leaves the private network.
	NodeSubnet string

	// IPRange is the private network's range. The route below needs its
	// gateway, and Hetzner puts that at the range's first address.
	IPRange string

	// AllowSchedulingOnControlPlanes is required on a cluster with no worker
	// pool, where the control plane is the only place a pod can run.
	AllowSchedulingOnControlPlanes bool
}

// PrivateInterface is the NIC a Hetzner server gets its private address on.
//
// Hardcoded, because Talos offers no way to say "the interface on the private
// network": deviceSelector matches on hardware, and both NICs here are virtio.
// Hetzner attaches the public one first, so the private one is the second.
//
// If this is ever wrong the route below lands on the wrong interface and
// pod-to-pod across nodes stops working — the exact failure this patch
// exists to fix, which is why cluster:smoke now asks about it directly.
const PrivateInterface = "eth1"

// NetworkGateway is the gateway of a Hetzner private network: the first
// address of its range.
//
// Derived rather than configured. It was going to be the literal "10.0.0.1",
// which is right only while network.ipRange keeps its default — and a topology
// that moves the range would have got a route pointing into a network it is not
// on, with pod traffic silently leaving through the public interface.
func NetworkGateway(ipRange string) (string, error) {
	if ipRange == "" {
		return "", errors.New("network.ipRange is required to derive the private network's gateway")
	}

	prefix, err := netip.ParsePrefix(ipRange)
	if err != nil {
		return "", fmt.Errorf("network.ipRange %q: %w", ipRange, err)
	}

	// Masked first: a range written as 10.0.0.5/16 means the 10.0.0.0/16
	// network, and its gateway is 10.0.0.1 rather than 10.0.0.6.
	return prefix.Masked().Addr().Next().String(), nil
}

// KubePrismPort is the node-local API load balancer Talos enables, and the
// port Cilium is pointed at.
//
// One value, three halves that never call each other: this package writes it
// into the machine config's features.kubePrism, layers/10-node-platform hands
// it to Cilium as k8sServicePort, and layers/20-network-policy permits egress
// to it. A mismatch does not fail an apply — Cilium comes up pointing at a port
// Talos does not listen on, so there is no service dataplane at all and every
// ClusterIP blackholes with nothing saying why.
//
// It was 7445 twice: the constant in internal/pkg/chartsettings and a bare
// literal below, with a comment in the layer claiming they were the same thing.
// Nothing compared them.
//
// It then lived in internal/pkg/clusterref, on the argument that unlike
// internal/pkg/hetzner that package "pulls no provider SDK, so
// internal/pkg/chartsettings can read it without dragging the Hetzner and Talos
// SDKs into a Helm template's build graph". The argument was right and the
// address was wrong: the provider SDKs are three packages each, and what they
// sit on is Pulumi's own SDK — 768 packages, which clusterref pulls as surely
// as hetzner does. Here it costs nothing, because nothing in this package
// needs Pulumi at all.
const KubePrismPort = 7445

// BuildClusterPatch renders the shared machine-config patch.
//
// Two choices here are deliberate and coupled to the layers above:
//
//   - The CNI is set to "none". Talos would otherwise install Flannel, which
//     would then have to be removed before Cilium could take over. The
//     layers/10-node-platform owns the CNI, so the cluster ships without one and
//     stay NotReady until that layer runs. That is the intended state, not a
//     failure.
//   - kube-proxy is disabled. Cilium replaces it in eBPF; running both means
//     two components programming the same service dataplane.
//
// The external cloud provider setting is what leaves nodes carrying the
// `uninitialized` taint until the hcloud CCM starts. That taint is the
// mechanism that stops workloads landing on a node before its addresses and
// routes are configured.
func BuildClusterPatch(args ClusterPatchArgs) (string, error) {
	if args.PodCIDR == "" || args.ServiceCIDR == "" {
		return "", fmt.Errorf("cluster patch: podCIDR and serviceCIDR are required")
	}

	if args.NodeSubnet == "" {
		return "", fmt.Errorf("cluster patch: nodeSubnet is required to pin kubelet's node IP to the private network")
	}

	gateway, err := NetworkGateway(args.IPRange)
	if err != nil {
		return "", fmt.Errorf("cluster patch: %w", err)
	}

	patch := map[string]any{
		"machine": map[string]any{
			"kubelet": map[string]any{
				"extraArgs": map[string]string{
					// Hands node lifecycle to the hcloud CCM: it clears the
					// uninitialized taint, sets provider IDs and programmes
					// routes for the pod network.
					"cloud-provider": "external",
					// Ask the cluster CA for a serving certificate instead of
					// self-signing one.
					//
					// Talos self-signs the kubelet's serving certificate and
					// it carries no IP SANs, so anything connecting to
					// tcp/10250 by address cannot verify it. metrics-server
					// does exactly that, fails every scrape with "cannot
					// validate certificate for 10.0.1.2 because it doesn't
					// contain any IP SANs", never becomes Ready, and Helm
					// waits out its whole timeout — which is how this was
					// found, after a 611-second install that rolled back.
					//
					// This makes the kubelet issue a CSR that something has to
					// approve. 30-cluster-services deploys the approver, and metrics-server
					// follows it. The alternative, --kubelet-insecure-tls, is
					// what both Talos and metrics-server call testing-only.
					"rotate-server-certificates": "true",
				},
				"nodeIP": map[string]any{
					"validSubnets": []string{args.NodeSubnet},
				},
			},
			"network": map[string]any{
				"interfaces": []map[string]any{{
					// The private NIC. Named rather than selected, because
					// Talos's deviceSelector cannot say "the one on the private
					// network" and Hetzner presents the public NIC first.
					"interface": PrivateInterface,
					// Kept, and load-bearing. Hetzner serves the private
					// address over DHCP; declaring the interface without this
					// turns it off and the node loses its private address —
					// which is every path this repository depends on.
					"dhcp": true,
					"routes": []map[string]any{{
						// THE ROUTE THIS WHOLE STANZA EXISTS FOR.
						//
						// Pod traffic to another node has to reach the private
						// network's gateway, because the gateway is what holds
						// the per-node routes the hcloud CCM programmes. The
						// node's own table has none: eth1 is a /32 and the
						// only on-link peer is the gateway, so without this a
						// packet for another node's pod CIDR matches the
						// DEFAULT route and leaves through the public
						// interface, where it is dropped.
						//
						// This is what was missing. Cilium was asked to fill
						// the gap with autoDirectNodeRoutes and could not —
						// "must be directly reachable" — so for as long as the
						// cluster had more than one node, pod-to-pod across
						// nodes had no route at all. Everything downstream
						// followed: CoreDNS unreachable from another node, the
						// CSI controller unable to resolve api.hetzner.cloud,
						// and a driver killed by its own liveness probe every
						// twenty seconds.
						//
						// Installed in BOTH routing modes on purpose. Under
						// tunnel it is inert — Cilium's per-node routes to
						// cilium_vxlan are more specific and win — and keeping
						// it there means switching modes is one Helm value
						// rather than a Talos apply to every node.
						"network": args.PodCIDR,
						"gateway": gateway,
					}},
				}},
			},
			"features": map[string]any{
				// Node-scoped kubelet credentials: a compromised node can read
				// only its own secrets rather than every secret in the cluster.
				"kubePrism": map[string]any{
					"enabled": true,
					// The port Cilium is pointed at, from the one place both
					// halves read it. It was a literal here and a constant in
					// internal/pkg/chartsettings, with nothing comparing them.
					"port": KubePrismPort,
				},
			},
		},
		"cluster": map[string]any{
			"network": map[string]any{
				"cni":            map[string]any{"name": "none"},
				"podSubnets":     []string{args.PodCIDR},
				"serviceSubnets": []string{args.ServiceCIDR},
			},
			"proxy": map[string]any{"disabled": KubeProxyDisabled},
			"controllerManager": map[string]any{
				"extraArgs": map[string]string{
					"cloud-provider": "external",
				},
			},
			// No cloud-provider on the API server. It never needed one — the
			// external provider is the kubelet's and the controller manager's
			// business — and Kubernetes removed the flag, so passing it is
			// fatal rather than merely redundant:
			//
			//	kube-apiserver: Error: unknown flag: --cloud-provider
			//
			// The static pod then exits on every restart, the scheduler fails
			// behind it unable to reach the API through KubePrism, and the
			// cluster comes up with etcd and kubelet healthy and port 6443
			// refusing connections. No offline check catches this: talosctl
			// validates the shape of the config, not whether a flag exists in
			// the Kubernetes version the config pins.
			"allowSchedulingOnControlPlanes": args.AllowSchedulingOnControlPlanes,
		},
	}

	// The audit policy, which replaces the `level: Metadata` one Talos ships.
	//
	// Merged in rather than written above: it is a document of its own, it is
	// static, and internal/pkg/clusterspec/auditpolicy.yaml is where it can be
	// read against Kubernetes' own examples and diffed when it changes.
	//
	// The error is not decoration. Talos passes this through unstructured and
	// the API server ignores fields it does not recognise, so the checks in
	// AuditPolicy are the only thing between a misspelt selector and a rule
	// that quietly matches nobody — and `talosctl validate` will not say a
	// word, for the same reason the cloud-provider note above gives.
	auditPolicy, err := AuditPolicy()
	if err != nil {
		return "", fmt.Errorf("cluster patch: %w", err)
	}

	cluster, ok := patch["cluster"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("cluster patch: the cluster section is not a map")
	}

	cluster["apiServer"] = map[string]any{"auditPolicy": auditPolicy}

	rendered, err := marshalPatch(patch)
	if err != nil {
		return "", err
	}

	encryption, err := buildVolumeEncryption()
	if err != nil {
		return "", err
	}

	return rendered + encryption, nil
}

// buildVolumeEncryption renders a VolumeConfig document per encrypted system
// volume.
//
// Separate documents rather than `machine.systemDiskEncryption`, which is the
// v1alpha1 spelling of the same thing: v1.13 documents these as VolumeConfig,
// and mixing the two forms for one volume is a conflict rather than a
// duplicate.
//
// `nodeID` as the key: it derives from the node's UUID, so a restored snapshot
// or a volume attached to another machine cannot be read. That is the threat
// worth buying here. It is NOT protection from someone who can already run
// commands on the node — for that Talos wants `tpm`, which needs SecureBoot
// and a TPM that a Hetzner Cloud instance does not have, or `kms`, which needs
// a key server this repository does not run.
func buildVolumeEncryption() (string, error) {
	out := ""

	for _, volume := range []string{VolumeSTATE, VolumeEPHEMERAL} {
		document, err := marshalPatch(map[string]any{
			"apiVersion": "v1alpha1",
			"kind":       "VolumeConfig",
			"name":       volume,
			"encryption": map[string]any{
				"provider": EncryptionProvider,
				"keys": []map[string]any{
					{
						"nodeID": map[string]any{},
						"slot":   EncryptionKeySlot,
					},
				},
			},
		})
		if err != nil {
			return "", err
		}

		out += "---\n" + document
	}

	return out, nil
}

// NodePatchArgs describes the per-node patch.
type NodePatchArgs struct {
	// Hostname becomes the Kubernetes node name.
	Hostname string

	// CertSANs are the names signed into this node's certificates.
	//
	// All three matter: the node's own reachable address (for operators
	// running talosctl), its private address (for in-cluster clients), and
	// the cluster endpoint — which on an HA cluster is the load balancer,
	// and is the name kubectl actually verifies. Omitting the last one
	// produces a cluster that works through a node and fails TLS through
	// the load balancer.
	CertSANs []string

	// NodeLabels and NodeTaints are applied by kubelet at registration.
	NodeLabels map[string]string
	NodeTaints []string
}

// BuildNodePatch renders the per-node machine-config patch.
func BuildNodePatch(args NodePatchArgs) (string, error) {
	if args.Hostname == "" {
		return "", fmt.Errorf("node patch: hostname is required")
	}

	if len(args.CertSANs) == 0 {
		return "", fmt.Errorf("node patch: at least one certificate SAN is required, or nothing can verify this node")
	}

	kubelet := map[string]any{}

	if len(args.NodeLabels) > 0 {
		kubelet["extraConfig"] = map[string]any{}
		kubelet["nodeLabels"] = args.NodeLabels
	}

	if len(args.NodeTaints) > 0 {
		taints := make(map[string]string, len(args.NodeTaints))

		for _, raw := range args.NodeTaints {
			key, value, effect, err := ParseTaint(raw)
			if err != nil {
				return "", fmt.Errorf("node patch: %w", err)
			}

			// Talos spells node taints as key: value:Effect.
			taints[key] = value + ":" + effect
		}

		kubelet["nodeTaints"] = taints
	}

	machine := map[string]any{
		"certSANs": dedupe(args.CertSANs),
	}

	if len(kubelet) > 0 {
		machine["kubelet"] = kubelet
	}

	patch := map[string]any{
		"machine": machine,
		"cluster": map[string]any{
			// Talos signs the cluster endpoint into the apiserver certificate
			// on its own, but not the individual node addresses. Setting both
			// lists is what makes kubectl work through a node AND through the
			// load balancer.
			"apiServer": map[string]any{"certSANs": dedupe(args.CertSANs)},
		},
	}

	rendered, err := marshalPatch(patch)
	if err != nil {
		return "", err
	}

	// The hostname goes in its own document, NOT in machine.network.
	//
	// Talos rejects `machine.network.hostname` outright — "static hostname is
	// already set in v1alpha1 config" — because a HostnameConfig document is
	// always present, defaulting to `auto: stable`. Setting both is the
	// conflict, so the automatic mode has to be turned off in the same
	// document that sets the name. Measured against talosctl 1.13.10:
	// `auto: "off"` is the only spelling accepted; "", "none" and "disabled"
	// are all rejected as not belonging to AutoHostnameKind.
	hostname := map[string]any{
		"apiVersion": "v1alpha1",
		"kind":       "HostnameConfig",
		"auto":       "off",
		"hostname":   args.Hostname,
	}

	hostnameDoc, err := marshalPatch(hostname)
	if err != nil {
		return "", err
	}

	return rendered + "---\n" + hostnameDoc, nil
}

func marshalPatch(patch map[string]any) (string, error) {
	raw, err := yaml.Marshal(patch)
	if err != nil {
		return "", fmt.Errorf("marshal machine config patch: %w", err)
	}

	return string(raw), nil
}

// dedupe removes repeats while keeping first-seen order. Certificate SANs
// commonly repeat — on a single-node cluster the node address and the cluster
// endpoint are the same string — and a duplicated SAN is accepted but makes
// the certificate harder to read.
func dedupe(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))

	for _, value := range values {
		if value == "" {
			continue
		}

		if _, dup := seen[value]; dup {
			continue
		}

		seen[value] = struct{}{}

		out = append(out, value)
	}

	return out
}

// BuildEtcdPatch pins etcd's peer traffic to the private network.
//
// Its own document, applied to control planes only, because Talos refuses the
// section anywhere else — measured by `task cluster:machine-config:check`, which
// rejected it in the shared cluster patch with
//
//	etcd config is only allowed on control plane machines
//
// Why it is needed at all: without it etcd advertises whichever address a node
// has first, and on Hetzner that is the public one. The perimeter firewall
// opens tcp/6443 and tcp/50000 to adminCIDRs and nothing else, so the members
// cannot reach each other's tcp/2380. Measured on the first real three-member
// cluster this repository built: two members, one of them a learner for ever,
// and the third never joining at all.
//
// A single node never needed it, which is why a commented HA configuration
// could look complete for months.
func BuildEtcdPatch(nodeSubnet string) (string, error) {
	if nodeSubnet == "" {
		return "", fmt.Errorf("etcd patch: nodeSubnet is required to keep peer traffic off the public address")
	}

	patch := map[string]any{
		"cluster": map[string]any{
			"etcd": map[string]any{
				"advertisedSubnets": []string{nodeSubnet},
			},
		},
	}

	encoded, err := yaml.Marshal(patch)
	if err != nil {
		return "", fmt.Errorf("etcd patch: %w", err)
	}

	return string(encoded), nil
}
