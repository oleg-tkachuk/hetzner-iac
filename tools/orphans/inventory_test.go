package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
)

// fixtureDir holds list responses recorded in the API's own shape, one file
// per path with its slashes as underscores.
const fixtureDir = "testdata/hcloud"

// snapshotQuery is the filter readInventory must send with the image listing.
const snapshotQuery = "type=snapshot"

// forbidden is the API's error body for a token that may not list a kind.
// Not a retried code, so a failing listing fails at once.
const forbidden = `{"error":{"code":"forbidden","message":"insufficient permissions"}}`

// apiPaths are every listing readInventory makes, with the noun its error
// names.
func apiPaths() map[string]string {
	return map[string]string{
		"/volumes":                       "list volumes: ",
		"/load_balancers":                "list load balancers: ",
		"/servers":                       "list servers: ",
		"/primary_ips":                   "list primary ips: ",
		"/images":                        "list snapshots: ",
		"/floating_ips":                  "list floating ips: ",
		"/networks":                      "list networks: ",
		"/firewalls":                     "list firewalls: ",
		"/placement_groups":              "list placement groups: ",
		"/ssh_keys":                      "list ssh keys: ",
		"/certificates":                  "list certificates: ",
		"/zones":                         "list dns zones: ",
		"/storage_boxes":                 "list storage boxes: ",
		"/storage_boxes/701/subaccounts": "list subaccounts of storage box backups: ",
	}
}

// hcloudServer answers every listing from the fixtures, and failing with an
// API error.
func hcloudServer(t *testing.T, failing string) *hcloud.Client {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.URL.Path == "/images" && !strings.Contains(r.URL.RawQuery, snapshotQuery) {
			t.Errorf("images listed without %s: %q", snapshotQuery, r.URL.RawQuery)
		}

		if r.URL.Path == failing {
			w.WriteHeader(http.StatusForbidden)

			if _, err := w.Write([]byte(forbidden)); err != nil {
				t.Error(err)
			}

			return
		}

		name := strings.ReplaceAll(strings.TrimPrefix(r.URL.Path, "/"), "/", "_") + ".json"

		body, err := os.ReadFile(filepath.Join(fixtureDir, name))
		if err != nil {
			t.Errorf("no fixture for %s: %v", r.URL.Path, err)
			w.WriteHeader(http.StatusNotFound)

			return
		}

		if _, err := w.Write(body); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)

	// Storage boxes live on the Hetzner API rather than the Cloud API; both
	// point here so no request leaves the test.
	return hcloud.NewClient(
		hcloud.WithEndpoint(server.URL), hcloud.WithHetznerEndpoint(server.URL),
		hcloud.WithToken("test"), hcloud.WithRetryOpts(hcloud.RetryOpts{MaxRetries: 0}),
	)
}

func TestReadInventory_ReadsEveryKindFromTheAPI(t *testing.T) {
	t.Parallel()

	got, err := readInventory(t.Context(), hcloudServer(t, ""))
	require.NoError(t, err)

	ours := map[string]string{clusterspec.LabelCluster: "platform-dev"}

	// The size is the field the first version decoded as zero.
	assert.Equal(t, []Volume{{ID: 101, Name: "pvc-kept", SizeGB: 50}}, got.Volumes)
	assert.Equal(t, []LoadBalancer{{
		ID: 201, Name: "ingress", Labels: map[string]string{ServiceUIDLabel: "uid-kept"},
	}}, got.LoadBalancers)
	assert.Equal(t, []Server{
		{
			ID: 301, Name: "platform-dev-control-plane-0", Type: "cx32",
			Labels: map[string]string{clusterspec.LabelCluster: "platform-dev"},
		},
		// No server type is a server with no type, not a crash.
		{ID: 302, Name: "rescue-bake", Labels: map[string]string{}},
	}, got.Servers)
	assert.Equal(t, []PrimaryIP{{ID: 401, Name: "spare", IP: "203.0.113.7"}}, got.PrimaryIPs)
	assert.Equal(t, []Snapshot{{
		ID: 501, Description: "talos v1.13.9", SizeGB: 0.5,
		Labels: map[string]string{TalosVersionLabel: "v1.13.9"},
	}}, got.Snapshots)
	assert.Equal(t, []Resource{
		{KindFloatingIP, 601, "egress", ours},
		{KindNetwork, 602, "platform-dev", ours},
		{KindFirewall, 603, "nodes", ours},
		{KindPlacementGroup, 604, "spread", ours},
		{KindSSHKey, 605, "operator", map[string]string{}},
		{KindCertificate, 606, "ingress-tls", ours},
		{KindZone, 607, "example.com", map[string]string{}},
		{KindStorageBox, 701, "backups", ours},
		{KindSubaccount, 702, "u1-sub1", ours},
	}, got.Resources)
}

func TestReadInventory_NamesTheListingThatFailed(t *testing.T) {
	t.Parallel()

	for path, want := range apiPaths() {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			_, err := readInventory(t.Context(), hcloudServer(t, path))

			require.Error(t, err)
			assert.True(t, strings.HasPrefix(err.Error(), want), "%q does not start %q", err, want)
			assert.Contains(t, err.Error(), "insufficient permissions")
		})
	}
}

func TestListAs_NothingListedIsNil(t *testing.T) {
	t.Parallel()

	// Nil as before the listings shared a helper, not an empty slice.
	got, err := listAs(t.Context(), "volumes", func(context.Context) ([]*hcloud.Volume, error) {
		return []*hcloud.Volume{}, nil
	}, toVolume)

	require.NoError(t, err)
	assert.Nil(t, got)
}
