package main

import (
	"net"
	"testing"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func server(name, ipv4 string) *hcloud.Server {
	s := &hcloud.Server{Name: name}
	if ipv4 != "" {
		s.PublicNet.IPv4.IP = net.ParseIP(ipv4)
	}

	return s
}

func TestAddresses_SortedByServerName(t *testing.T) {
	t.Parallel()

	got, err := addresses([]*hcloud.Server{
		server("platform-dev-control-plane-2", "203.0.113.12"),
		server("platform-dev-control-plane-0", "203.0.113.10"),
		server("platform-dev-control-plane-1", "203.0.113.11"),
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"203.0.113.10", "203.0.113.11", "203.0.113.12"}, got)
}

func TestAddresses_RefusesNoServers(t *testing.T) {
	t.Parallel()

	// An empty answer is a wrong cluster name or a wrong token, and a restore
	// that loops over nothing reports success.
	_, err := addresses(nil)

	require.Error(t, err)
}

func TestAddresses_RefusesAServerWithoutAPublicIPv4(t *testing.T) {
	t.Parallel()

	// network.publicIPv4: false is a supported topology. The CLI's columns
	// printed a placeholder for such a node, and the restore loop would have
	// targeted it as an address.
	_, err := addresses([]*hcloud.Server{
		server("platform-dev-control-plane-0", "203.0.113.10"),
		server("platform-dev-control-plane-1", ""),
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "platform-dev-control-plane-1")
}

func TestRun_TakesAStackAndACluster(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{nil, {"dev"}, {"dev", ""}, {"", "platform-dev"}, {"dev", "platform-dev", "extra"}} {
		assert.ErrorIs(t, run(t.Context(), args), errUsage, "%q", args)
	}
}
