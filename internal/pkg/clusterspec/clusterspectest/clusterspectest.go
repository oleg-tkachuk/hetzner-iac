// Package clusterspectest holds the topology fixture two packages' tests need.
//
// A separate package because it takes *testing.T, which has no business in the
// code that builds a real cluster — the same reason net/http/httptest is not
// net/http, and the reason internal/pkg/layer/layertest exists.
//
// One fixture rather than one per package. The topology that internal/pkg/
// hetzner builds a cluster from has to be a topology internal/pkg/clusterspec
// accepts, and two copies of it drift the moment the schema gains a required
// field: the copy the schema's own tests use is updated, and the copy the
// cluster's tests use keeps passing against a validator that no longer agrees
// with it.
package clusterspectest

import (
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"

	"github.com/stretchr/testify/require"
)

// Valid is a topology with every field a cluster needs and nothing more: three
// control-plane nodes, one worker pool, and an API load balancer, which is the
// shape the HA paths are written against.
const Valid = `
apiVersion: hetzner-iac/v1
kind: Cluster
metadata:
  name: platform-hel
placement:
  location: hel1
  networkZone: eu-central
network:
  adminCIDRs:
    - 203.0.113.4/32
talos:
  version: v1.14.0
controlPlane:
  count: 3
  serverType: cx23
  apiLoadBalancerType: lb11
workerPools:
  - name: worker
    count: 2
    serverType: cx33
`

// MustParse parses a topology or fails the test.
//
// Through ParseTopology rather than a struct literal, so the defaults and the
// validation a real load applies are applied here too: a fixture built by hand
// is a topology no file could produce.
func MustParse(t *testing.T, raw string) *clusterspec.Topology {
	t.Helper()

	topology, err := clusterspec.ParseTopology([]byte(raw))
	require.NoError(t, err)

	return topology
}

// MustParseValid is MustParse over Valid, which is what most callers want.
func MustParseValid(t *testing.T) *clusterspec.Topology {
	t.Helper()

	return MustParse(t, Valid)
}
