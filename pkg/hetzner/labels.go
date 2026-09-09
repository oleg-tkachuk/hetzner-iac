package hetzner

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Label keys stamped on every hcloud resource this package creates.
//
// They are not decoration. LabelCluster is what the firewall's label selector
// matches, which is how a node created later inherits the perimeter without a
// diff on the firewall itself, and how `hcloud server list -l` scopes to one
// cluster on a shared project.
const (
	LabelCluster   = "cluster"
	LabelRole      = "role"
	LabelPool      = "pool"
	LabelManagedBy = "managed-by"

	RoleControlPlane = "control-plane"
	RoleWorker       = "worker"

	managedByValue = "hetzner-iac"
)

// ResourceLabels builds the label set for a cluster-scoped resource. extra
// wins over the defaults so a caller can override a role, but never the
// cluster name — that would detach the resource from its firewall.
func ResourceLabels(cluster string, extra map[string]string) map[string]string {
	labels := map[string]string{
		LabelCluster:   cluster,
		LabelManagedBy: managedByValue,
	}

	for key, value := range extra {
		if key == LabelCluster {
			continue
		}

		labels[key] = value
	}

	return labels
}

// ClusterSelector is the label selector matching every server in a cluster.
func ClusterSelector(cluster string) string {
	return fmt.Sprintf("%s=%s", LabelCluster, cluster)
}

// Ports opened on the public interface of a Talos node.
const (
	PortKubeAPI   = 6443  // kube-apiserver
	PortTalosdAPI = 50000 // talosctl → apid: config apply, upgrades, kubeconfig

	protocolTCP  = "tcp"
	protocolICMP = "icmp"
	directionIn  = "in"
)

// FirewallRule is a provider-neutral inbound rule.
//
// Only inbound is modelled: an empty outbound set means Hetzner permits all
// egress, which is what a cluster pulling images and reaching ACME needs.
type FirewallRule struct {
	Description string
	Protocol    string
	Port        string
	SourceIPs   []string
}

// FirewallRuleOptions selects which of the optional rules to include.
type FirewallRuleOptions struct {
	// AllowICMP opens ping from the admin CIDRs. Cheap, and the first thing
	// wanted when debugging routing.
	AllowICMP bool
	// Extra rules are appended verbatim.
	Extra []FirewallRule
}

// BuildFirewallRules produces the perimeter rule set for a cluster.
//
// Scope note worth keeping in mind when reading the result: Hetzner Cloud
// firewalls apply to the PUBLIC interface only. Node-to-node traffic over the
// private network is never filtered, so there are deliberately no
// intra-cluster rules here — adding them would imply a protection that does
// not exist.
func BuildFirewallRules(adminCIDRs []string, opts FirewallRuleOptions) ([]FirewallRule, error) {
	if len(adminCIDRs) == 0 {
		return nil, ErrEmptyAdminCIDRs
	}

	rules := []FirewallRule{
		{
			Description: "kube-apiserver",
			Protocol:    protocolTCP,
			Port:        strconv.Itoa(PortKubeAPI),
			SourceIPs:   adminCIDRs,
		},
		{
			Description: "talos apid",
			Protocol:    protocolTCP,
			Port:        strconv.Itoa(PortTalosdAPI),
			SourceIPs:   adminCIDRs,
		},
	}

	if opts.AllowICMP {
		rules = append(rules, FirewallRule{
			Description: "icmp",
			Protocol:    protocolICMP,
			SourceIPs:   adminCIDRs,
		})
	}

	rules = append(rules, opts.Extra...)

	for i, rule := range rules {
		if err := rule.validate(i); err != nil {
			return nil, err
		}
	}

	return rules, nil
}

func (r FirewallRule) validate(index int) error {
	if len(r.SourceIPs) == 0 {
		// An empty source list is refused rather than widened: "no sources"
		// and "the whole internet" must never be the same thing.
		return fmt.Errorf("firewall rule %d (%s) has no source CIDRs", index, r.Description)
	}

	switch r.Protocol {
	case protocolTCP, "udp":
		if r.Port == "" {
			return fmt.Errorf("firewall rule %d (%s) is %s and needs a port", index, r.Description, r.Protocol)
		}
	case protocolICMP, "gre", "esp":
		if r.Port != "" {
			return fmt.Errorf("firewall rule %d (%s) is %s and must not carry port %q", index, r.Description, r.Protocol, r.Port)
		}
	default:
		return fmt.Errorf("firewall rule %d (%s) has unsupported protocol %q", index, r.Description, r.Protocol)
	}

	return nil
}

// NodeName is the hcloud server name for a node. It is also the Kubernetes
// node name, so it has to survive DNS-1123 and stay stable: the ordinal is
// positional, never a hash of the node's attributes.
func NodeName(cluster, pool string, ordinal int) string {
	return fmt.Sprintf("%s-%s-%d", cluster, pool, ordinal)
}

// SortedLabelPairs renders labels as sorted key=value pairs. Sorted so that a
// map's random iteration order cannot produce a spurious diff on every run.
func SortedLabelPairs(labels map[string]string) []string {
	pairs := make([]string, 0, len(labels))
	for key, value := range labels {
		pairs = append(pairs, key+"="+value)
	}

	sort.Strings(pairs)

	return pairs
}

// ParseTaint splits a key=value:Effect taint into its parts.
func ParseTaint(taint string) (key, value, effect string, err error) {
	eq := strings.Index(taint, "=")
	colon := strings.LastIndex(taint, ":")

	if eq < 0 || colon < eq {
		return "", "", "", fmt.Errorf("taint %q must be key=value:Effect", taint)
	}

	return taint[:eq], taint[eq+1 : colon], taint[colon+1:], nil
}
