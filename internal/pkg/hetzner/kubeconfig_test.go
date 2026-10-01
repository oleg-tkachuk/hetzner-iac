package hetzner

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	nodeServer = "https://203.0.113.10:6443"
	kubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: https://10.0.1.250:6443
    certificate-authority-data: Y2E=
users:
- name: admin@test
  user:
    client-certificate-data: Y2VydA==
contexts:
- name: admin@test
  context: {cluster: test, user: admin@test}
current-context: admin@test
`
)

func TestPointKubeconfigAt_ChangesOnlyTheServer(t *testing.T) {
	t.Parallel()

	out, err := pointKubeconfigAt(kubeconfig, nodeServer)
	require.NoError(t, err)

	config, err := clientcmd.Load([]byte(out))
	require.NoError(t, err)

	assert.Equal(t, nodeServer, config.Clusters["test"].Server)
	assert.Equal(t, []byte("ca"), config.Clusters["test"].CertificateAuthorityData)
	assert.Equal(t, []byte("cert"), config.AuthInfos["admin@test"].ClientCertificateData)
	assert.Equal(t, "admin@test", config.CurrentContext)
}

func TestPointKubeconfigAt_RefusesWhatItCannotPoint(t *testing.T) {
	t.Parallel()

	_, err := pointKubeconfigAt("apiVersion: v1\nkind: Config\n", nodeServer)
	require.Error(t, err, "a kubeconfig with no cluster would be exported pointing nowhere")

	_, err = pointKubeconfigAt("clusters: [", nodeServer)
	require.Error(t, err)
}
