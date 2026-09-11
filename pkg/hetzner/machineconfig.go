package hetzner

import (
	"fmt"

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

	// AllowSchedulingOnControlPlanes is required on a cluster with no worker
	// pool, where the control plane is the only place a pod can run.
	AllowSchedulingOnControlPlanes bool
}

// BuildClusterPatch renders the shared machine-config patch.
//
// Two choices here are deliberate and coupled to the layers above:
//
//   - The CNI is set to "none". Talos would otherwise install Flannel, which
//     would then have to be removed before Cilium could take over. The
//     10-cni layer owns the CNI, so the cluster ships without one and nodes
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
					// approve. 30-core deploys the approver, and metrics-server
					// follows it. The alternative, --kubelet-insecure-tls, is
					// what both Talos and metrics-server call testing-only.
					"rotate-server-certificates": "true",
				},
				"nodeIP": map[string]any{
					"validSubnets": []string{args.NodeSubnet},
				},
			},
			"features": map[string]any{
				// Node-scoped kubelet credentials: a compromised node can read
				// only its own secrets rather than every secret in the cluster.
				"kubePrism": map[string]any{
					"enabled": true,
					"port":    7445,
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

	return marshalPatch(patch)
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
