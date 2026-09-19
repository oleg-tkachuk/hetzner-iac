// Package values holds every chart's Helm values as a template file, and
// renders them.
//
// The values used to be pulumi.Map literals inside each layer's main.go, which
// made editing one of them a Go edit and a recompile, and made reading a whole
// chart's configuration a matter of following nested map literals. They are
// YAML now, in files beside this one, which is the form the charts document
// and the form an operator can diff against upstream's values.yaml.
//
// What that costs, and how it is paid for:
//
//   - A YAML key is no longer a Go identifier, so the compiler cannot catch a
//     misspelling. TestTemplates_MentionEverySettingThatFailsSilently holds
//     the templates to the constants in internal/pkg/charts, and
//     `charts:render-check` asserts the effect those keys must have on
//     the rendered chart — which is what actually catches a typo, since Helm
//     accepts an unknown key in silence.
//   - A template cannot see a Pulumi output, because an output has no value
//     when the program builds its inputs. Asset takes the resolved data
//     instead: the layer applies the outputs it needs and hands the result
//     over, so the dependency still reaches the engine.
//
// Rendered with missingkey=error. A template naming a field the data does not
// have is a failure rather than the empty string — `<no value>` in a values
// file is a chart running on a default nobody chose.
package values

import (
	"bytes"
	"embed"
	"fmt"
	"strings"
	"text/template"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// Extension is what a values template is called, so the tests and the render
// check agree on how to find one.
const Extension = ".yaml.tmpl"

//go:embed *.yaml.tmpl
var files embed.FS

// Names lists every chart that has a values template, sorted by embed.
func Names() ([]string, error) {
	entries, err := files.ReadDir(".")
	if err != nil {
		return nil, fmt.Errorf("read values templates: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, chartOf(entry.Name()))
	}

	return names, nil
}

// chartOf is the registry key a template file is named after.
func chartOf(file string) string {
	return file[:len(file)-len(Extension)]
}

// Source returns a template's text, for a test or a check that needs to read
// it rather than render it.
func Source(chart string) (string, error) {
	raw, err := files.ReadFile(chart + Extension)
	if err != nil {
		return "", fmt.Errorf("no values template for chart %q: %w", chart, err)
	}

	return string(raw), nil
}

// helpers are the template functions a values file may use.
//
// Only indent, and only because one chart takes a whole configuration file as
// a values string: Alloy's collector config is multi-line, and YAML needs it
// indented under the key that holds it.
var helpers = template.FuncMap{
	"indent": func(spaces int, text string) string {
		pad := strings.Repeat(" ", spaces)

		lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
		for i, line := range lines {
			if line != "" {
				lines[i] = pad + line
			}
		}

		return strings.Join(lines, "\n")
	},
}

// Render produces one chart's values YAML.
func Render(chart string, data any) (string, error) {
	text, err := Source(chart)
	if err != nil {
		return "", err
	}

	parsed, err := template.New(chart).Funcs(helpers).Option("missingkey=error").Parse(text)
	if err != nil {
		return "", fmt.Errorf("values template %q: %w", chart, err)
	}

	var out bytes.Buffer
	if err := parsed.Execute(&out, data); err != nil {
		return "", fmt.Errorf("render values for %q: %w", chart, err)
	}

	return out.String(), nil
}

// Static renders a chart whose values need nothing from the cluster, ready to
// hand to a Helm release.
func Static(chart string, data any) (pulumi.AssetOrArchiveArrayInput, error) {
	rendered, err := Render(chart, data)
	if err != nil {
		return nil, err
	}

	return pulumi.AssetOrArchiveArray{pulumi.NewStringAsset(rendered)}, nil
}

// Asset renders a chart whose values need something the cluster tier
// published — a name, a CIDR, a count.
//
// data resolves to the value the template is executed against. Threading the
// outputs through it rather than reading them inside the template is what
// keeps the dependency visible to the engine: the release waits for the same
// outputs it would have waited for as map inputs.
func Asset(chart string, data pulumi.Output) pulumi.AssetOrArchiveArrayInput {
	return pulumi.AssetOrArchiveArray{
		// Unchecked, and the last one in this package. ApplyT's result type
		// follows from the callback's signature three lines below, so the
		// assertion cannot be wrong while that signature is in view. The typed
		// pulumix form does not fit: it takes pulumix.Input[T], and `data` is
		// the pulumi.Output interface that layer.Component.ValuesFrom returns.
		data.ApplyT(func(resolved any) (pulumi.AssetOrArchive, error) {
			rendered, err := Render(chart, resolved)
			if err != nil {
				return nil, err
			}

			return pulumi.NewStringAsset(rendered), nil
		}).(pulumi.AssetOrArchiveOutput),
	}
}
