package hetzner

import (
	"fmt"

	"k8s.io/client-go/tools/clientcmd"
)

// pointKubeconfigAt returns the kubeconfig with every cluster's server set to
// the given URL, leaving the credentials and the CA as they are.
func pointKubeconfigAt(raw, server string) (string, error) {
	config, err := clientcmd.Load([]byte(raw))
	if err != nil {
		return "", fmt.Errorf("read kubeconfig: %w", err)
	}

	if len(config.Clusters) == 0 {
		return "", fmt.Errorf("kubeconfig names no cluster to point at %s", server)
	}

	for _, cluster := range config.Clusters {
		cluster.Server = server
	}

	out, err := clientcmd.Write(*config)
	if err != nil {
		return "", fmt.Errorf("write kubeconfig: %w", err)
	}

	return string(out), nil
}
