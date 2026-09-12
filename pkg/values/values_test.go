package values_test

import (
	"strings"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/charts"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/chartsettings"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/values"

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

func TestTemplates_AreValidYAMLOnceRendered(t *testing.T) {
	t.Parallel()

	// Rendered with the zero value of nothing in particular: what is being
	// checked is the structure, and a template that only parses as YAML when
	// a value happens to be non-empty is a template that breaks on an empty
	// config key.
	names, err := values.Names()
	require.NoError(t, err)

	for _, name := range names {
		text, err := values.Source(name)
		require.NoError(t, err)

		// Strip the actions rather than execute them: this test is about the
		// surrounding YAML, and each layer's own test renders with real data.
		var out map[string]any
		assert.NoError(t, yaml.Unmarshal([]byte(blankActions(text)), &out), "%s is not yaml", name)
	}
}

// blankActions replaces every {{ ... }} with a placeholder scalar.
func blankActions(text string) string {
	var out strings.Builder

	for {
		start := strings.Index(text, "{{")
		if start < 0 {
			out.WriteString(text)

			return out.String()
		}

		end := strings.Index(text[start:], "}}")
		if end < 0 {
			out.WriteString(text)

			return out.String()
		}

		out.WriteString(text[:start])
		out.WriteString("placeholder")

		text = text[start+end+len("}}"):]
	}
}

func TestTemplates_MentionEverySettingThatFailsSilently(t *testing.T) {
	t.Parallel()

	// The guarantee the move to templates could have lost. A Helm key in a
	// YAML file is not a Go identifier, so nothing compiles it — but
	// pkg/chartsettings holds the keys whose misspelling leaves a default in
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
