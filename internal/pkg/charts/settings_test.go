package charts_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/charts"
)

func TestEffects_AreComplete(t *testing.T) {
	t.Parallel()

	effects := charts.Effects()
	require.NotEmpty(t, effects)

	for _, effect := range effects {
		label := effect.Chart + " " + effect.Expect

		assert.NotEmpty(t, effect.Expect, label)
		// The release and the namespace are filled in from the chart now, so
		// these assert the derivation rather than the declaration — which is
		// what a row used to get wrong by repeating them.
		assert.NotEmpty(t, effect.Release, label)
		assert.NotEmpty(t, effect.Namespace, label)
		// The reason is printed on failure. Without it the report says a
		// string is absent and nothing about what that costs.
		assert.NotEmpty(t, effect.Why, label)
	}
}

func TestEffects_NameRegisteredCharts(t *testing.T) {
	t.Parallel()

	for _, effect := range charts.Effects() {
		chart, err := charts.Get(effect.Chart)
		require.NoError(t, err, effect.Chart)

		// The effect renders in the namespace the layer installs into, or the
		// rendered content can legitimately differ.
		assert.Equal(t, chart.Namespace, effect.Namespace, effect.Chart)
	}
}

func TestCiliumEffects_CoverTheKubePrismPort(t *testing.T) {
	t.Parallel()

	// Cilium reaching the API through anything but KubePrism ties the CNI to
	// one control-plane node's life, so the port must be asserted.
	want := strconv.Itoa(charts.KubePrismPort)

	covered := false

	for _, effect := range charts.Effects() {
		if effect.Chart == charts.Cilium && strings.Contains(effect.Expect, want) {
			covered = true
		}
	}

	assert.True(t, covered, "no cilium effect asserts the KubePrism port %s", want)
}

func TestKeys_AreNotEmpty(t *testing.T) {
	t.Parallel()

	// An empty key would match anything in a template and make the template
	// tests that look for it vacuous.
	for name, key := range map[string]string{
		"CiliumKubeProxyReplacement": charts.CiliumKubeProxyReplacement,
		"CiliumK8sServiceHost":       charts.CiliumK8sServiceHost,
		"CiliumK8sServicePort":       charts.CiliumK8sServicePort,
		"HcloudCSIDefaultLocation":   charts.HcloudCSIDefaultLocation,
		"TraefikPorts":               charts.TraefikPorts,
		"TraefikEntryPointWeb":       charts.TraefikEntryPointWeb,
		"TraefikEntryPointTLS":       charts.TraefikEntryPointTLS,
		"TraefikProxyProtocol":       charts.TraefikProxyProtocol,
		"TraefikTrustedIPs":          charts.TraefikTrustedIPs,
		"MetricsServerAddressTypes":  charts.MetricsServerAddressTypes,
		"PriorityClassName":          charts.PriorityClassName,
	} {
		assert.NotEmpty(t, key, name)
		assert.NotContains(t, key, " ", "%s must not contain a space", name)
	}
}

// TestEffects_CoverEveryChartThatHasASilentSetting is what the move makes
// possible: the settings live with the chart, so a chart that acquires one and
// declares no effect is visible here rather than in a cluster.
func TestEffects_CoverEveryChartThatHasASilentSetting(t *testing.T) {
	t.Parallel()

	byChart := map[string]int{}
	for _, effect := range charts.Effects() {
		byChart[effect.Chart]++
	}

	// Argo CD and external-secrets set nothing that fails silently — their
	// values are replica counts and resource limits, wrong in a diff. Named
	// rather than counted, so acquiring one is a decision somebody records.
	for _, key := range charts.Keys() {
		if key == charts.ArgoCD || key == charts.ExternalSecrets {
			assert.Zero(t, byChart[key],
				"%s declares an effect now: drop it from this exemption", key)

			continue
		}

		assert.Positive(t, byChart[key],
			"%s declares no effect. Every other chart here sets at least one value whose "+
				"misspelling Helm accepts in silence", key)
	}
}
