package clusterspec

import "strings"

// The committed topology of one stack is cluster.<stack>.yaml. infra/cluster
// loads it by that name, and every tool that finds topologies on disk reads
// the stack back out of it, so the rule is spelled here once.
const (
	topologyFilePrefix = "cluster."
	topologyFileSuffix = ".yaml"
)

// ExampleTopologyStack is the stack the committed template is named for. It
// is validated like any other topology and applied by nobody.
const ExampleTopologyStack = "example"

// TopologyFile names the committed file that describes one stack's cluster.
func TopologyFile(stack string) string {
	return topologyFilePrefix + stack + topologyFileSuffix
}

// TopologyStack returns the stack a topology file name carries, and false for
// a name TopologyFile would not have built.
func TopologyStack(name string) (string, bool) {
	// The suffix before the prefix: the other order turns "cluster.yaml",
	// which names no stack, into the stack "yaml".
	base, hasSuffix := strings.CutSuffix(name, topologyFileSuffix)
	stack, hasPrefix := strings.CutPrefix(base, topologyFilePrefix)

	if !hasSuffix || !hasPrefix || stack == "" || strings.Contains(stack, ".") {
		return "", false
	}

	return stack, true
}
