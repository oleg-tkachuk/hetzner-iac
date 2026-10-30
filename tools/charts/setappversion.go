package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/charts"
)

// chartsDir holds one declaration file per chart, named for its registry key.
var chartsDir = filepath.Join("internal", "pkg", "charts")

// chartSourceMode is a declaration's mode when rewritten: a committed text file.
const chartSourceMode = 0o644

var errNoAppVersionField = errors.New("no AppVersion field beside the pin")

// writeAppVersion sets AppVersion, and the `// app <version>` comment beside
// Version, in the file that declares the chart.
func writeAppVersion(key string, chart charts.Chart, appVersion string) error {
	return writeAppVersionIn(chartsDir, key, chart, appVersion)
}

// writeAppVersionIn is writeAppVersion for declarations in dir, so a test can
// rewrite a copy.
func writeAppVersionIn(dir, key string, chart charts.Chart, appVersion string) error {
	path := filepath.Join(dir, key+".go")

	// #nosec G304 -- a registry key inside this repository, not user input.
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	out, err := setAppVersion(src, chart, appVersion)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	return os.WriteFile(path, out, chartSourceMode) // #nosec G306,G703 -- a committed text file at a registry path
}

// setAppVersion rewrites one chart's AppVersion in its declaration's text.
//
// The match keys off Name → Repo → Version → AppVersion, the order Renovate's
// own pattern already relies on, and must be exact: the bytes on those lines
// are replaced in place so the change is two lines, not a reformatted file.
//
// It refuses rather than guesses where the declaration has no AppVersion, or
// upstream publishes none: adding or removing the field is a change to what
// the registry claims, and that is for a person to make.
func setAppVersion(src []byte, chart charts.Chart, appVersion string) ([]byte, error) {
	if appVersion == "" {
		return nil, fmt.Errorf("%s %s publishes no app version; remove AppVersion by hand", chart.Name, chart.Version)
	}

	pattern := regexp.MustCompile(
		`(Name:\s+"` + regexp.QuoteMeta(chart.Name) + `",\s*\n` +
			`\s*Repo:\s+"` + regexp.QuoteMeta(chart.Repo) + `",\s*\n` +
			`\s*Version:\s+"` + regexp.QuoteMeta(chart.Version) + `",)[^\n]*\n` +
			`(\s*AppVersion:\s+)"[^"]*",`)

	if found := len(pattern.FindAllIndex(src, -1)); found != 1 {
		return nil, fmt.Errorf("%s %s: %w (found %d)", chart.Name, chart.Version, errNoAppVersionField, found)
	}

	// Spliced by index rather than through a replacement template, so nothing
	// in the version is ever read as a `$` reference.
	at := pattern.FindSubmatchIndex(src)

	out := append([]byte{}, src[:at[0]]...)
	out = append(out, src[at[2]:at[3]]...)
	out = append(out, " // app "+appVersion+"\n"...)
	out = append(out, src[at[4]:at[5]]...)
	out = append(out, `"`+appVersion+`",`...)

	return append(out, src[at[1]:]...), nil
}
