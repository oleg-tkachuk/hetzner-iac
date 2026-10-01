package hetzner

import (
	"errors"
	"fmt"

	"k8s.io/client-go/tools/clientcmd"
)

// contextUserPrefix is how Talos names a kubeconfig's user and context:
// admin@<cluster>. The extra contexts follow it with the node's name.
const contextUserPrefix = "admin@"

var errNoNodes = errors.New("no control-plane node to point the kubeconfig at")

// kubeconfigNode is one control-plane node an operator can reach the API on.
type kubeconfigNode struct {
	// Name names the node's cluster and context; the first node keeps the
	// names Talos gave, so the current context is the one it always was.
	Name   string
	Server string
}

// clientKubeconfig returns the kubeconfig an operator uses: Talos's, with its
// server set to the first node, plus a cluster and a context for every other
// control-plane node. kubectl does not fail over, so when the first node is
// down another is one `kubectl config use-context` away. The credentials and
// the CA are Talos's, unchanged, and shared by every context.
func clientKubeconfig(raw string, nodes []kubeconfigNode) (string, error) {
	if len(nodes) == 0 {
		return "", errNoNodes
	}

	config, err := clientcmd.Load([]byte(raw))
	if err != nil {
		return "", fmt.Errorf("read kubeconfig: %w", err)
	}

	current, ok := config.Contexts[config.CurrentContext]
	if !ok {
		return "", fmt.Errorf("kubeconfig has no current context %q to point at %s", config.CurrentContext, nodes[0].Server)
	}

	cluster, ok := config.Clusters[current.Cluster]
	if !ok {
		return "", fmt.Errorf("kubeconfig's current context names no cluster %q", current.Cluster)
	}

	cluster.Server = nodes[0].Server

	for _, node := range nodes[1:] {
		copied := cluster.DeepCopy()
		copied.Server = node.Server

		context := current.DeepCopy()
		context.Cluster = node.Name

		config.Clusters[node.Name] = copied
		config.Contexts[contextUserPrefix+node.Name] = context
	}

	out, err := clientcmd.Write(*config)
	if err != nil {
		return "", fmt.Errorf("write kubeconfig: %w", err)
	}

	return string(out), nil
}

// kubeconfigNodes pairs each control-plane node's name with its API URL.
func kubeconfigNodes(names, servers []string) []kubeconfigNode {
	nodes := make([]kubeconfigNode, 0, len(names))
	for i, name := range names {
		nodes = append(nodes, kubeconfigNode{Name: name, Server: servers[i]})
	}

	return nodes
}
