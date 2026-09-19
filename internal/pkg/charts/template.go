package charts

import (
	"bytes"
	"embed"
	"fmt"
	"strings"
	"text/template"
)

// Every chart's Helm values, as a template file beside its declaration.
//
// The values used to be pulumi.Map literals inside each layer's main.go, which
// made editing one a Go edit and a recompile, and made reading a whole chart's
// configuration a matter of following nested map literals. They are YAML now,
// in files beside the chart that names them — the form the charts document and
// the form an operator can diff against upstream's values.yaml.
//
// Here rather than in internal/pkg/values, where they were first written, for
// the reason that package gave for holding them: a template and the fields it
// names are one thing. That argument reaches one step further than it did —
// the template, the fields, the pin, the layer and the expected workloads are
// all one thing, and this is where the rest of them already live.
//
// What it costs, and how it is paid for:
//
//   - A YAML key is no longer a Go identifier, so the compiler cannot catch a
//     misspelling. TestTemplates_MentionEverySettingThatFailsSilently holds
//     the templates to the Setting constants each chart declares, and
//     `charts:render-check` asserts the effect those keys must have on the
//     rendered chart — which is what actually catches a typo, since Helm
//     accepts an unknown key in silence.
//   - A template cannot see a Pulumi output, because an output has no value
//     when the program builds its inputs. internal/pkg/values.Asset takes the
//     resolved data instead: the layer applies the outputs it needs and hands
//     the result over, so the dependency still reaches the engine. That half
//     stays there, because it is the half that needs the SDK — and this
//     package must not reach it.
//
// Rendered with missingkey=error. A template naming a field the data does not
// have is a failure rather than the empty string — `<no value>` in a values
// file is a chart running on a default nobody chose.

// Extension is what a values template is called, so the tests and the render
// check agree on how to find one.
const Extension = ".yaml.tmpl"

//go:embed *.yaml.tmpl
var templates embed.FS

// TemplateNames lists every chart that has a values template, sorted by embed.
func TemplateNames() ([]string, error) {
	entries, err := templates.ReadDir(".")
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
	raw, err := templates.ReadFile(chart + Extension)
	if err != nil {
		return "", fmt.Errorf("no values template for chart %q: %w", chart, err)
	}

	return string(raw), nil
}

// helpers are the template functions a values file may use.
//
// Only indent, and only because one chart takes a whole configuration file as
// a values string: a multi-line collector config, which YAML needs indented
// under the key that holds it.
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

// Probe returns representative data for a chart's template.
//
// An unknown chart is an error rather than nil: nil renders a template whose
// fields all resolve to nothing, which is a values file full of empty strings
// and a check that passes on it. A chart with no probe returns nil data and no
// error, because a template that needs nothing is a real case.
func Probe(chart string) (any, error) {
	definition, err := Lookup(chart)
	if err != nil {
		return nil, err
	}

	if definition.Probe == nil {
		return nil, nil
	}

	return definition.Probe(), nil
}
