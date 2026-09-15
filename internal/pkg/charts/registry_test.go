package charts_test

import (
	"strings"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/charts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistry_EveryChartIsValid(t *testing.T) {
	t.Parallel()

	all := charts.All()
	require.NotEmpty(t, all)

	for key, chart := range all {
		t.Run(key, func(t *testing.T) {
			t.Parallel()

			require.NoError(t, chart.Validate())
		})
	}
}

func TestRegistry_NoFloatingVersions(t *testing.T) {
	t.Parallel()

	// The property that makes the platform reproducible: a chart must resolve
	// to the same artifact next month as it does today.
	for key, chart := range charts.All() {
		assert.NotContains(t, strings.ToLower(chart.Version), "latest", "chart %q", key)
		assert.NotEmpty(t, chart.Version, "chart %q", key)
	}
}

func TestValidate_RejectsFloatingAndMalformed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		chart   charts.Chart
		wantMsg string
	}{
		{
			name:    "floating tag",
			chart:   charts.Chart{Name: "x", Repo: "https://example.test", Namespace: "default", Version: "latest"},
			wantMsg: "not an exact version",
		},
		{
			name:    "major-only pin",
			chart:   charts.Chart{Name: "x", Repo: "https://example.test", Namespace: "default", Version: "1"},
			wantMsg: "not an exact version",
		},
		{
			name:    "minor-only pin",
			chart:   charts.Chart{Name: "x", Repo: "https://example.test", Namespace: "default", Version: "1.2"},
			wantMsg: "not an exact version",
		},
		{
			name:    "no repository",
			chart:   charts.Chart{Name: "x", Namespace: "default", Version: "1.2.3"},
			wantMsg: "has no repository",
		},
		{
			name:    "no namespace",
			chart:   charts.Chart{Name: "x", Repo: "https://example.test", Version: "1.2.3"},
			wantMsg: "has no namespace",
		},
		{
			name:    "no name",
			chart:   charts.Chart{Repo: "https://example.test", Namespace: "default", Version: "1.2.3"},
			wantMsg: "chart name is empty",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.chart.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantMsg)
		})
	}
}

func TestValidate_AcceptsBothVersionSpellings(t *testing.T) {
	t.Parallel()

	// cert-manager tags its chart with a leading v; most others do not.
	// Accepting both is deliberate, and a test stops someone "tidying" one
	// spelling into a version that does not exist upstream.
	for _, version := range []string{"1.21.1", "v1.21.1", "90.0.0", "2.10.0-rc.1"} {
		chart := charts.Chart{Name: "x", Repo: "https://example.test", Namespace: "default", Version: version}
		assert.NoError(t, chart.Validate(), version)
	}
}

func TestGet(t *testing.T) {
	t.Parallel()

	cilium, err := charts.Get("cilium")
	require.NoError(t, err)
	assert.Equal(t, "cilium", cilium.Name)
	assert.Equal(t, "kube-system", cilium.Namespace)

	_, err = charts.Get("nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown chart")
}

func TestAll_ReturnsACopy(t *testing.T) {
	t.Parallel()

	// A caller mutating the returned map must not be able to repoint a chart
	// for every other caller in the process.
	all := charts.All()
	all["cilium"] = charts.Chart{Name: "hijacked"}

	cilium, err := charts.Get("cilium")
	require.NoError(t, err)
	assert.Equal(t, "cilium", cilium.Name)
}

func TestKeys_IsSorted(t *testing.T) {
	t.Parallel()

	keys := charts.Keys()
	require.NotEmpty(t, keys)

	for i := 1; i < len(keys); i++ {
		assert.LessOrEqual(t, keys[i-1], keys[i], "Keys must be sorted for stable output")
	}
}

func TestRegistry_CoversEveryLayer(t *testing.T) {
	t.Parallel()

	// A layer referencing a chart that was never registered fails at apply.
	// Listing the expectation here makes a removal a test failure instead.
	for _, key := range []string{
		"cilium",
		"hcloud-ccm", "hcloud-csi",
		"cert-manager", "external-secrets", "metrics-server",
		"traefik",
		"argo-cd",
	} {
		_, err := charts.Get(key)
		assert.NoError(t, err, "chart %q is referenced by a layer", key)
	}
}
