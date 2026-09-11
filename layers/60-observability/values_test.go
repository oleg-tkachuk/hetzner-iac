package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// update rewrites the render values files from this layer instead of checking
// them: `go test ./layers/60-observability/ -update`.
var update = flag.Bool("update", false, "rewrite tools/charts/values from this layer")

// renderValuesDir is where tools/charts looks for a chart's values.
const renderValuesDir = "../../tools/charts/values"

// generatedHeader opens every file this test writes.
const generatedHeader = `# Generated from layers/60-observability. Do not edit.
#
# Regenerate with: go test ./layers/60-observability/ -update
#
# tools/charts renders offline — no Pulumi run, so nothing can resolve a
# layer's inputs — and reads these files instead. Generated rather than
# maintained by hand, because the copy kept by hand is the copy that drifts:
# alloy's file was missing the one key that decided whether its DaemonSet
# could start, and the render check passed on a chart the cluster then refused.
`

// renderValues is every chart this layer installs, against the values it
// installs with.
//
// Per-stack keys resolve to their defaults here. That is not a gap: retention
// and volume sizes change no rendered object, and a values file cannot hold
// one stack's answer anyway.
func renderValues() map[string]pulumi.Map {
	return map[string]pulumi.Map{
		"kube-prometheus-stack": PrometheusValues(DefaultRetention, DefaultMetricsSize),
		"loki":                  LokiValues(),
		"tempo":                 TempoValues(),
		"alloy":                 AlloyValues(),
	}
}

func TestRenderValues_AreTheValuesTheLayerDeploys(t *testing.T) {
	for chart, values := range renderValues() {
		t.Run(chart, func(t *testing.T) {
			path := filepath.Join(renderValuesDir, chart+".yaml")

			body, err := yaml.Marshal(plainValues(t, values))
			require.NoError(t, err)

			if *update {
				require.NoError(t, os.WriteFile(path, append([]byte(generatedHeader), body...), 0o600))

				return
			}

			raw, err := os.ReadFile(path)
			require.NoError(t, err,
				"tools/charts renders %s with this file; without it the check validates chart defaults", chart)

			// Compared as trees, not as text: the header is a comment and
			// yaml does not promise the same bytes across library versions.
			var got, want map[string]any

			require.NoError(t, yaml.Unmarshal(raw, &got))
			require.NoError(t, yaml.Unmarshal(body, &want))

			assert.Equal(t, want, got,
				"tools/charts/values/%s.yaml is not what this layer deploys — regenerate with `go test ./layers/60-observability/ -update`", chart)
		})
	}
}

// plainValues projects a layer's values onto the plain Go values a YAML file
// can hold.
//
// It fails on anything else on purpose. A pulumi.Output cannot be resolved
// outside a Pulumi run, so a values function that grows one has no honest
// projection — and silently dropping the key would leave the render check
// validating something the cluster never sees, which is the whole failure
// this generation exists to end.
func plainValues(t *testing.T, value any) any {
	t.Helper()

	switch typed := value.(type) {
	case pulumi.Map:
		plain := make(map[string]any, len(typed))
		for key, item := range typed {
			plain[key] = plainValues(t, item)
		}

		return plain

	case pulumi.Array:
		plain := make([]any, 0, len(typed))
		for _, item := range typed {
			plain = append(plain, plainValues(t, item))
		}

		return plain

	case pulumi.StringArray:
		plain := make([]any, 0, len(typed))
		for _, item := range typed {
			plain = append(plain, plainValues(t, item))
		}

		return plain

	case pulumi.String:
		return string(typed)

	case pulumi.Bool:
		return bool(typed)

	case pulumi.Int:
		return int(typed)

	default:
		t.Fatalf("values hold a %T, which no values file can carry", value)

		return nil
	}
}
