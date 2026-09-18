package chartsettings_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/charts"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/chartsettings"
)

func TestEffects_AreComplete(t *testing.T) {
	t.Parallel()

	require.NotEmpty(t, chartsettings.Effects)

	for _, effect := range chartsettings.Effects {
		label := effect.Chart + " " + strings.Join(effect.Set, " ")

		assert.NotEmpty(t, effect.Set, label)
		assert.NotEmpty(t, effect.Expect, label)
		assert.NotEmpty(t, effect.Release, label)
		assert.NotEmpty(t, effect.Namespace, label)
		// The reason is printed on failure. Without it the report says a
		// string is absent and nothing about what that costs.
		assert.NotEmpty(t, effect.Why, label)
	}
}

func TestEffects_NameRegisteredCharts(t *testing.T) {
	t.Parallel()

	for _, effect := range chartsettings.Effects {
		chart, err := charts.Get(effect.Chart)
		require.NoError(t, err, effect.Chart)

		// The effect renders in the namespace the layer installs into, or the
		// rendered content can legitimately differ.
		assert.Equal(t, chart.Namespace, effect.Namespace, effect.Chart)
	}
}

func TestEffects_AssertOnOutputNotInput(t *testing.T) {
	t.Parallel()

	// The whole point: Helm accepts an unknown key silently, so an effect that
	// merely echoed its own --set expression would pass while the chart
	// ignored it. Expect must name something the chart's own template emits.
	for _, effect := range chartsettings.Effects {
		for _, set := range effect.Set {
			assert.NotEqual(t, set, effect.Expect,
				"effect for %s asserts its own input, which proves nothing", effect.Chart)
		}
	}
}

func TestCiliumPortEffect_SetsTheHostToo(t *testing.T) {
	t.Parallel()

	// Measured: Cilium renders the API port ONLY when the host is set as well.
	// An effect that set the port alone would assert against an empty render
	// and fail for the wrong reason.
	var portEffect *chartsettings.Effect

	for i, effect := range chartsettings.Effects {
		if effect.Chart == "cilium" && strings.Contains(strings.Join(effect.Set, " "), chartsettings.CiliumK8sServicePort) {
			portEffect = &chartsettings.Effects[i]
		}
	}

	require.NotNil(t, portEffect, "the KubePrism port must be covered")

	joined := strings.Join(portEffect.Set, " ")
	assert.Contains(t, joined, chartsettings.CiliumK8sServiceHost)
	assert.Contains(t, portEffect.Expect, strconv.Itoa(chartsettings.KubePrismPort))
}

func TestKeys_AreNotEmpty(t *testing.T) {
	t.Parallel()

	// An empty key would render as `--set =value`, which helm rejects — but
	// only at run time, and only for the charts that happen to be checked.
	for name, key := range map[string]string{
		"CiliumKubeProxyReplacement": chartsettings.CiliumKubeProxyReplacement,
		"CiliumK8sServiceHost":       chartsettings.CiliumK8sServiceHost,
		"CiliumK8sServicePort":       chartsettings.CiliumK8sServicePort,
		"TraefikPorts":               chartsettings.TraefikPorts,
		"TraefikEntryPointWeb":       chartsettings.TraefikEntryPointWeb,
		"TraefikEntryPointTLS":       chartsettings.TraefikEntryPointTLS,
		"TraefikProxyProtocol":       chartsettings.TraefikProxyProtocol,
		"TraefikTrustedIPs":          chartsettings.TraefikTrustedIPs,
		"MetricsServerAddressTypes":  chartsettings.MetricsServerAddressTypes,
	} {
		assert.NotEmpty(t, key, name)
		assert.NotContains(t, key, " ", "%s must not contain a space", name)
	}
}
