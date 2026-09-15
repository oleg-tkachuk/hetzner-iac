package values_test

import (
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/charts"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/chartsettings"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/values"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func TestNames_AreAllPinnedCharts(t *testing.T) {
	t.Parallel()

	// A template named after nothing in the registry is a file nobody reads:
	// the layer asks for values by the registry key, so a misnamed file
	// silently leaves the chart on its defaults.
	names, err := values.Names()
	require.NoError(t, err)
	require.NotEmpty(t, names, "no values templates found — the embed pattern must be wrong")

	for _, name := range names {
		_, err := charts.Get(name)
		assert.NoError(t, err, "values template %q names no pinned chart", name)
	}
}

func TestSource_RefusesAnUnknownChart(t *testing.T) {
	t.Parallel()

	_, err := values.Source("no-such-chart")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no values template")
}

func TestRender_RefusesAFieldTheDataDoesNotHave(t *testing.T) {
	t.Parallel()

	// The property that makes a template safe to edit. Without it a renamed
	// field renders as the empty string and the chart runs on a default
	// nobody chose — the same silent class as a misspelt Helm key.
	names, err := values.Names()
	require.NoError(t, err)

	_, err = values.Render(names[0], struct{ Nothing string }{})
	require.Error(t, err, "a template rendered against the wrong data must fail")
}

func TestTemplates_RenderValidYAMLFromProbeData(t *testing.T) {
	t.Parallel()

	// Every template, rendered the way `task charts:render-check` renders it.
	// A template that only parses as YAML when a value happens to be non-empty
	// is a template that breaks on an empty config key — and one whose action
	// supplies an indented block, as Alloy's collector config does, cannot be
	// checked any other way.
	names, err := values.Names()
	require.NoError(t, err)

	for _, name := range names {
		probe, err := values.Probe(name)
		require.NoError(t, err, "every template needs probe data, or the render check cannot render it")

		text, err := values.Render(name, probe)
		require.NoError(t, err)

		var out map[string]any
		assert.NoError(t, yaml.Unmarshal([]byte(text), &out), "%s does not render valid yaml", name)
		assert.NotEmpty(t, out, "%s renders nothing", name)
	}
}

func TestProbe_RefusesAnUnknownChart(t *testing.T) {
	t.Parallel()

	// nil would render a template whose every field resolves to nothing —
	// a values file of empty strings, and a check that passes on it.
	_, err := values.Probe("no-such-chart")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no probe data")
}

func TestTemplates_MentionEverySettingThatFailsSilently(t *testing.T) {
	t.Parallel()

	// The guarantee the move to templates could have lost. A Helm key in a
	// YAML file is not a Go identifier, so nothing compiles it — but
	// internal/pkg/chartsettings holds the keys whose misspelling leaves a default in
	// place with nothing said, and this asserts each one is still spelled the
	// way the render check will look for it.
	traefik, err := values.Source("traefik")
	require.NoError(t, err)

	for _, key := range []string{
		chartsettings.TraefikPorts,
		chartsettings.TraefikEntryPointWeb,
		chartsettings.TraefikEntryPointTLS,
		chartsettings.TraefikProxyProtocol,
		chartsettings.TraefikTrustedIPs,
	} {
		assert.Contains(t, traefik, key,
			"the traefik template no longer spells %q the way the render check asserts it", key)
	}
}

func TestArgoCD_AnUnsetDomainStaysAnEmptyString(t *testing.T) {
	t.Parallel()

	// The regression this exists for, and it cost seventeen minutes of a
	// deploy before the release rolled back.
	//
	// `domain: {{ .Domain }}` renders as YAML null when the domain is unset,
	// and this chart interpolates null straight into argocd-cm — measured
	// against the chart itself:
	//
	//     domain: ""   ->  url: https://
	//     domain:      ->  url: https://%!s(<nil>)
	//
	// The server then runs with a nonsense URL, never becomes available, and
	// Helm waits out its whole timeout with the cause nowhere in the output.
	rendered, err := values.Render("argo-cd", values.ArgoCD{
		Domain: "", IngressClass: "traefik", Issuer: "letsencrypt", Replicas: 2,
	})
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(rendered), &out))

	global, ok := out["global"].(map[string]any)
	require.True(t, ok)

	domain, present := global["domain"]
	require.True(t, present, "global.domain must be set, not absent")
	assert.Equal(t, "", domain,
		"an unset domain must reach the chart as an empty string, never as null")
}
