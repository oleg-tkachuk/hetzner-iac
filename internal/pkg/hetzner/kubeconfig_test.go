package hetzner

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	talosContext = "admin@platform-dev"
	talosCluster = "platform-dev"
	kubeconfig   = `apiVersion: v1
kind: Config
clusters:
- name: platform-dev
  cluster:
    server: https://10.0.1.250:6443
    certificate-authority-data: Y2E=
users:
- name: admin@platform-dev
  user:
    client-certificate-data: Y2VydA==
contexts:
- name: admin@platform-dev
  context: {cluster: platform-dev, user: admin@platform-dev}
current-context: admin@platform-dev
`
)

func threeNodes() []kubeconfigNode {
	return []kubeconfigNode{
		{Name: "platform-dev-control-plane-0", Server: "https://203.0.113.10:6443"},
		{Name: "platform-dev-control-plane-1", Server: "https://203.0.113.11:6443"},
		{Name: "platform-dev-control-plane-2", Server: "https://203.0.113.12:6443"},
	}
}

func TestClientKubeconfig_OneContextPerControlPlaneNode(t *testing.T) {
	t.Parallel()

	nodes := threeNodes()

	out, err := clientKubeconfig(kubeconfig, nodes)
	require.NoError(t, err)

	config, err := clientcmd.Load([]byte(out))
	require.NoError(t, err)

	// The first node keeps Talos's names, so the current context is the one
	// every tool already used.
	assert.Equal(t, talosContext, config.CurrentContext)
	assert.Equal(t, nodes[0].Server, config.Clusters[talosCluster].Server)
	require.Len(t, config.Contexts, len(nodes))

	for _, node := range nodes[1:] {
		context := config.Contexts[contextUserPrefix+node.Name]
		require.NotNil(t, context, node.Name)
		assert.Equal(t, node.Name, context.Cluster)
		assert.Equal(t, talosContext, context.AuthInfo, "one credential for every context")
		assert.Equal(t, node.Server, config.Clusters[node.Name].Server)
		assert.Equal(t, []byte("ca"), config.Clusters[node.Name].CertificateAuthorityData)
	}

	assert.Len(t, config.AuthInfos, 1)
	assert.Equal(t, []byte("cert"), config.AuthInfos[talosContext].ClientCertificateData)
}

func TestClientKubeconfig_OneNodeChangesOnlyTheServer(t *testing.T) {
	t.Parallel()

	nodes := threeNodes()[:1]

	out, err := clientKubeconfig(kubeconfig, nodes)
	require.NoError(t, err)

	config, err := clientcmd.Load([]byte(out))
	require.NoError(t, err)

	assert.Len(t, config.Clusters, 1)
	assert.Len(t, config.Contexts, 1)
	assert.Equal(t, nodes[0].Server, config.Clusters[talosCluster].Server)
}

func TestClientKubeconfig_Refuses(t *testing.T) {
	t.Parallel()

	_, err := clientKubeconfig(kubeconfig, nil)
	require.ErrorIs(t, err, errNoNodes)

	_, err = clientKubeconfig("apiVersion: v1\nkind: Config\n", threeNodes())
	require.Error(t, err, "no current context means nothing to point anywhere")

	_, err = clientKubeconfig("clusters: [", threeNodes())
	require.Error(t, err)
}
