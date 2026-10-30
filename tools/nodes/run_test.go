package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
)

// fixtureDir holds server listings in the API's own shape.
const fixtureDir = "testdata/hcloud"

// Fixtures: three control-plane servers listed out of name order, two of
// which one has no public IPv4, and none at all.
const (
	threeServers      = "servers.json"
	serverWithoutIPv4 = "servers_without_ipv4.json"
	noServers         = "servers_none.json"
)

// testStack, testCluster and testToken are what the test passes and expects
// to see used.
const (
	testStack   = "dev"
	testCluster = "platform-dev"
	testToken   = "test-token"
)

// forbidden is the API's error body for a token that may not list servers.
// Not a retried code, so the listing fails at once.
const forbidden = `{"error":{"code":"forbidden","message":"insufficient permissions"}}`

var errNoToken = errors.New("no hcloud token in stack config")

// hcloudAPI answers the server listing from fixture, or with an API error when
// fixture is empty, and checks the request is the one run must make.
func hcloudAPI(t *testing.T, fixture string) []hcloud.ClientOption {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		assert.Equal(t, "/servers", r.URL.Path)
		assert.Equal(t, clusterspec.ControlPlaneSelector(testCluster), r.URL.Query().Get("label_selector"))
		assert.Equal(t, "Bearer "+testToken, r.Header.Get("Authorization"))

		if fixture == "" {
			w.WriteHeader(http.StatusForbidden)

			_, err := w.Write([]byte(forbidden))
			assert.NoError(t, err)

			return
		}

		body, err := os.ReadFile(filepath.Join(fixtureDir, fixture))
		if !assert.NoError(t, err) {
			w.WriteHeader(http.StatusNotFound)

			return
		}

		_, err = w.Write(body)
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	return []hcloud.ClientOption{hcloud.WithEndpoint(server.URL), hcloud.WithRetryOpts(hcloud.RetryOpts{MaxRetries: 0})}
}

// testDeps reads the token for testStack and talks to the fake API.
func testDeps(t *testing.T, fixture string) (deps, *bytes.Buffer) {
	t.Helper()

	var out bytes.Buffer

	return deps{
		token: func(_ context.Context, got string) (string, error) {
			assert.Equal(t, testStack, got)

			return testToken, nil
		},
		clientOptions: hcloudAPI(t, fixture),
		out:           &out,
	}, &out
}

func TestRun_PrintsEachAddressSortedByServerName(t *testing.T) {
	t.Parallel()

	d, out := testDeps(t, threeServers)

	require.NoError(t, run(t.Context(), []string{testStack, testCluster}, d))
	assert.Equal(t, "203.0.113.10\n203.0.113.11\n203.0.113.12\n", out.String())
}

func TestRun_Refusals(t *testing.T) {
	t.Parallel()

	selector := clusterspec.ControlPlaneSelector(testCluster)

	for _, tc := range []struct {
		name, fixture, want string
	}{
		{"no server matches", noServers, selector + ": no control-plane server matches"},
		{
			"a server has no public IPv4", serverWithoutIPv4,
			selector + ": server platform-dev-control-plane-1 has no public IPv4 address to reach it at",
		},
		{"the listing fails", "", "list servers matching " + selector + ": "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			d, out := testDeps(t, tc.fixture)

			err := run(t.Context(), []string{testStack, testCluster}, d)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Empty(t, out.String(), "an address printed alongside a refusal")
		})
	}
}

func TestRun_ATokenThatCannotBeReadStopsBeforeTheAPI(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	d := deps{
		token: func(context.Context, string) (string, error) { return "", errNoToken },
		clientOptions: []hcloud.ClientOption{hcloud.WithHTTPClient(&http.Client{
			Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
				t.Errorf("API called without a token: %s", r.URL)

				return nil, errNoToken
			}),
		})},
		out: &out,
	}

	require.ErrorIs(t, run(t.Context(), []string{testStack, testCluster}, d), errNoToken)
	assert.Empty(t, out.String())
}

// roundTripper is an http.RoundTripper from a function.
type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
