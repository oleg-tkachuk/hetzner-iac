package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/charts"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppVersionStatus(t *testing.T) {
	t.Parallel()

	// This is what a Renovate pull request runs into. Renovate bumps the chart
	// Version — the helm datasource knows nothing about app versions — so the
	// AppVersion beside it is stale until a human fixes it, and that is the
	// case this must catch.
	for name, tc := range map[string]struct {
		pinned, upstream string
		wantStatus       string
		wantAgrees       bool
	}{
		"equal":                {"3.6.12", "3.6.12", "ok", true},
		"stale after a bump":   {"3.6.12", "3.7.0", "WRONG", false},
		"both absent":          {"", "", "ok", true},
		"claims one, has none": {"1.0.0", "", "WRONG", false},
		"has one, claims none": {"", "1.0.0", "WRONG", false},
		"v prefix must match":  {"1.19.2", "v1.19.2", "WRONG", false},
	} {
		status, agrees := AppVersionStatus(tc.pinned, tc.upstream)

		assert.Equal(t, tc.wantStatus, status, name)
		assert.Equal(t, tc.wantAgrees, agrees, name)
	}
}

func TestDisplay_NamesAnAbsentVersion(t *testing.T) {
	t.Parallel()

	// A blank column in a report reads as a bug in the report.
	assert.Equal(t, "(none)", display(""))
	assert.Equal(t, "1.2.3", display("1.2.3"))
}

// The index reader is what tells an operator their pins are stale, so a
// misread index is a report that lies with confidence. The client and the
// repository URL are both parameters, so these exercise the real code against
// a local server rather than a mock of it.

// indexServer serves one body at /index.yaml, with the status given.
func indexServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/index.yaml" {
			w.WriteHeader(http.StatusNotFound)

			return
		}

		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))

	t.Cleanup(server.Close)

	return server
}

// A trimmed Helm repository index: two stable versions and a pre-release.
const testIndex = `
entries:
  traefik:
    - version: 41.5.0
      appVersion: v3.6.12
    - version: 41.4.0
      appVersion: v3.6.11
    - version: 42.0.0-rc.1
      appVersion: v3.7.0
`

func testChart(repo string) charts.Chart {
	return charts.Chart{Name: "traefik", Repo: repo, Version: "41.4.0", Namespace: "traefik"}
}

func TestFetchIndex_AppendsIndexYAMLWithOrWithoutATrailingSlash(t *testing.T) {
	t.Parallel()

	server := indexServer(t, http.StatusOK, testIndex)

	for name, repo := range map[string]string{
		"no trailing slash": server.URL,
		"trailing slash":    server.URL + "/",
	} {
		index, err := fetchIndex(t.Context(), server.Client(), repo)

		require.NoError(t, err, name)
		assert.Contains(t, index.Entries, "traefik", name)
	}
}

func TestFetchIndex_Errors(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		status int
		body   string
		want   string
	}{
		// A 404 served as an index would otherwise parse as an empty one, and
		// an empty index reads as "this chart does not exist upstream".
		"not found":    {http.StatusNotFound, "not found", "index returned"},
		"server error": {http.StatusInternalServerError, "boom", "index returned"},
		"unparseable":  {http.StatusOK, "entries: [this is not a map", "parse index"},
	} {
		server := indexServer(t, tc.status, tc.body)

		_, err := fetchIndex(t.Context(), server.Client(), server.URL)

		require.Error(t, err, name)
		assert.Contains(t, err.Error(), tc.want, name)
	}
}

func TestLatestInRepo_PicksTheLatestStable(t *testing.T) {
	t.Parallel()

	server := indexServer(t, http.StatusOK, testIndex)

	latest, err := latestInRepo(t.Context(), server.Client(), testChart(server.URL))

	require.NoError(t, err)
	// 42.0.0-rc.1 is newer and must not win: a pre-release pin is not
	// reproducible, which is the rule pkg/charts enforces on the other side.
	assert.Equal(t, "41.5.0", latest.String())
}

func TestLatestInRepo_Errors(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		body  string
		chart string
		want  string
	}{
		"chart absent from the index": {testIndex, "no-such-chart", "not in index"},
		// Every version is a pre-release, so there is nothing to report as
		// latest — distinct from "the chart is missing", and the report says
		// which.
		"nothing stable": {"entries:\n  traefik:\n    - version: 42.0.0-rc.1\n", "traefik", "no stable version"},
	} {
		server := indexServer(t, http.StatusOK, tc.body)

		chart := testChart(server.URL)
		chart.Name = tc.chart

		_, err := latestInRepo(t.Context(), server.Client(), chart)

		require.Error(t, err, name)
		assert.Contains(t, err.Error(), tc.want, name)
	}
}

func TestAppVersionInRepo_ReadsTheAppVersionOfThePinnedChartVersion(t *testing.T) {
	t.Parallel()

	server := indexServer(t, http.StatusOK, testIndex)

	// The pin is 41.4.0, not the newest: reading the newest entry's app
	// version is the mistake that would make every up-to-date pin look wrong.
	appVersion, err := appVersionInRepo(t.Context(), server.Client(), testChart(server.URL))

	require.NoError(t, err)
	assert.Equal(t, "v3.6.11", appVersion)
}

func TestAppVersionInRepo_SaysWhenThePinIsNotUpstreamAtAll(t *testing.T) {
	t.Parallel()

	server := indexServer(t, http.StatusOK, testIndex)

	chart := testChart(server.URL)
	chart.Version = "40.0.0"

	_, err := appVersionInRepo(t.Context(), server.Client(), chart)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in index", "a pin that does not exist is worse than a stale app version")
}
