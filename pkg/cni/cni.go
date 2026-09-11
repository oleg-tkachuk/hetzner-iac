// Package cni names the container network interfaces this platform can
// install, and what each one requires of the cluster underneath it.
//
// A CNI is not interchangeable the way two chart versions are, and the reason
// is in the cluster tier rather than in this layer: pkg/hetzner writes
// `proxy: {disabled: true}` into the Talos machine configuration, because
// Cilium replaces kube-proxy in eBPF and running both means two components
// programming the same service dataplane.
//
// So a CNI that does NOT replace kube-proxy cannot be installed on a cluster
// built that way — the cluster would have no service dataplane at all, every
// ClusterIP would blackhole, and nothing would say why. Each implementation
// here declares that property and Select refuses the mismatch, instead of
// leaving it to be discovered on a cluster that comes up and misbehaves.
//
// Adding one is therefore two changes, not one: an entry here, and a
// corresponding decision in the cluster tier. That is the honest cost, and
// naming it is the point of this package.
package cni

import (
	"fmt"
	"sort"
	"strings"
)

// Default is what a stack gets when it names no CNI.
const Default = "cilium"

// CNI is one implementation.
type CNI struct {
	// Chart is the key into pkg/charts.
	Chart string

	// ReplacesKubeProxy says whether this CNI provides the service dataplane
	// itself. It must match the cluster tier's choice: see
	// hetzner.KubeProxyDisabled.
	ReplacesKubeProxy bool
}

// implementations is the set. One entry, and that is not an oversight — a
// second one is a change to the cluster tier as well, and adding it without
// rendering and running it would be inventing values for something nobody
// here has booted.
var implementations = map[string]CNI{
	Default: {
		Chart:             "cilium",
		ReplacesKubeProxy: true,
	},
}

// Select returns the named CNI, or explains what is available.
//
// kubeProxyDisabled is what the cluster tier did, passed in rather than
// imported so this package stays free of the cluster tier and can be read on
// its own.
func Select(name string, kubeProxyDisabled bool) (CNI, error) {
	if name == "" {
		name = Default
	}

	chosen, ok := implementations[name]
	if !ok {
		return CNI{}, fmt.Errorf("unknown cni %q: this platform installs %s",
			name, strings.Join(Names(), ", "))
	}

	if kubeProxyDisabled && !chosen.ReplacesKubeProxy {
		return CNI{}, fmt.Errorf(
			"cni %q does not replace kube-proxy, but the cluster was built with it disabled.\n"+
				"That cluster would have no service dataplane at all and every ClusterIP would\n"+
				"blackhole silently. Either choose a cni that replaces it (%s), or change the\n"+
				"cluster tier's Talos machine configuration and rebuild the nodes",
			name, strings.Join(Names(), ", "))
	}

	return chosen, nil
}

// Names lists the implementations, sorted so an error message is stable.
func Names() []string {
	out := make([]string, 0, len(implementations))
	for name := range implementations {
		out = append(out, name)
	}

	sort.Strings(out)

	return out
}
