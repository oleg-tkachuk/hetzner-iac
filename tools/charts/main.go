package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/charts"

	"sigs.k8s.io/yaml"
)

func main() {
	if err := dispatch(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// defaultCommand is what runs when none is named.
const defaultCommand = "list"

// commands are the subcommands by name, each given the arguments after it.
var commands = map[string]func(args []string) error{
	defaultCommand: func([]string) error { return list() },
	"outdated":     func([]string) error { return outdated() },
	"render":       func([]string) error { return renderAll() },
	"appversions":  appversionsCommand,
	"repin":        func([]string) error { return repin(context.Background()) },
}

// dispatch runs the command the arguments name.
func dispatch(args []string) error {
	command := defaultCommand
	if len(args) > 0 {
		command, args = args[0], args[1:]
	}

	handler, known := commands[command]
	if !known {
		return fmt.Errorf("unknown command %q; use list, outdated, render, appversions or repin", command)
	}

	return handler(args)
}

func list() error {
	out := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(out, "CHART\tVERSION\tNAMESPACE\tREPO")

	for _, key := range charts.Keys() {
		chart := charts.MustGet(key)
		fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", key, chart.Version, chart.Namespace, chart.Repo)
	}

	return out.Flush()
}

// appversionsCommand parses `appversions [--write]`.
func appversionsCommand(args []string) error {
	write, err := appversionsWrite(args)
	if err != nil {
		return err
	}

	return appversions(write)
}

// writeFlag asks appversions to set what it finds wrong.
const writeFlag = "--write"

// appversionsWrite reads appversions' arguments: none, or the write flag.
func appversionsWrite(args []string) (bool, error) {
	switch {
	case len(args) == 0:
		return false, nil
	case len(args) == 1 && args[0] == writeFlag:
		return true, nil
	default:
		return false, fmt.Errorf("appversions takes only --write, got %q", strings.Join(args, " "))
	}
}

// indexTimeout bounds a whole report, so a hung registry cannot hang it; the
// client timeout alone does not cover a slow body.
const indexTimeout = 5 * time.Minute

// indexRequestTimeout bounds one index request.
const indexRequestTimeout = 30 * time.Second

// appversions checks that each entry's AppVersion is what the pinned chart
// actually ships, and with write sets it where it is not.
//
// Renovate bumps Version — the helm datasource only knows chart versions — and
// AppVersion beside it then becomes a lie: a comment claiming a chart deploys
// something it does not. Mostly that is a misleading document — but it stops
// being only a document the moment anything derives a value from it, as a
// validation image tag was derived from alloy's while that chart was pinned
// here.
//
// CI runs it as a gate. Renovate runs it with write as a post-upgrade task, so
// a chart bump arrives with AppVersion already following the pin: it used to
// stop for a person to set it, and every chart pull request went red for a
// value this command already knew. Write only changes a value that exists;
// adding or dropping the field still fails here for a person to decide.
func appversions(write bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), indexTimeout)
	defer cancel()

	var set appVersionSetter
	if write {
		set = writeAppVersion
	}

	return reportAppVersions(ctx, &http.Client{Timeout: indexRequestTimeout}, os.Stdout, os.Stderr, registryPins(), set)
}

// appVersionSetter writes a chart's app version into its declaration.
type appVersionSetter func(key string, chart charts.Chart, appVersion string) error

// reportAppVersions is appversions with its inputs passed in, so a test can
// run it against a local repository. A nil set only reports.
func reportAppVersions(
	ctx context.Context, client *http.Client, stdout, stderr io.Writer, pins []pin, set appVersionSetter,
) error {
	out := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(out, "CHART\tVERSION\tPINNED APP\tUPSTREAM APP\tSTATUS")

	var wrong []string

	for _, p := range pins {
		row, agrees := appVersionRow(ctx, client, p, set)
		if !agrees {
			wrong = append(wrong, p.Key)
		}

		fmt.Fprintln(out, row)
	}

	if err := out.Flush(); err != nil {
		return err
	}

	if len(wrong) > 0 {
		fmt.Fprintf(stderr,
			"\n%d chart entr(ies) claim the wrong app version.\n"+
				"Run `go run ./tools/charts appversions --write`, or set AppVersion in the chart's file\n"+
				"under internal/pkg/charts to the UPSTREAM APP column above, with the `// app <version>`\n"+
				"comment beside Version to match.\n",
			len(wrong))

		return fmt.Errorf("app version mismatch: %s", strings.Join(wrong, ", "))
	}

	return nil
}

// appVersionRow is one chart's row of the report, and whether its app
// version agrees with upstream once set has run.
func appVersionRow(ctx context.Context, client *http.Client, p pin, set appVersionSetter) (string, bool) {
	key, chart := p.Key, p.Chart

	upstream, err := appVersionInRepo(ctx, client, chart)
	if err != nil {
		return fmt.Sprintf("%s\t%s\t%s\t?\t%v", key, chart.Version, chart.AppVersion, err), false
	}

	status, agrees := AppVersionStatus(chart.AppVersion, upstream)
	if !agrees && set != nil {
		if setErr := set(key, chart, upstream); setErr != nil {
			status = setErr.Error()
		} else {
			status, agrees = "set", true
		}
	}

	return fmt.Sprintf("%s\t%s\t%s\t%s\t%s",
		key, chart.Version, display(chart.AppVersion), display(upstream), status), agrees
}

// AppVersionStatus compares a pinned app version against the upstream one.
//
// An empty upstream means the chart publishes none, which the registry records
// by leaving the field empty too. Claiming a version a chart does not publish
// is as wrong as claiming the wrong one.
func AppVersionStatus(pinned, upstream string) (status string, agrees bool) {
	if pinned == upstream {
		return "ok", true
	}

	return "WRONG", false
}

// display renders an empty version as something a reader can see.
func display(version string) string {
	if version == "" {
		return "(none)"
	}

	return version
}

// appVersionInRepo returns the app version the pinned chart version ships.
func appVersionInRepo(ctx context.Context, client *http.Client, chart charts.Chart) (string, error) {
	index, err := fetchIndex(ctx, client, chart.Repo)
	if err != nil {
		return "", err
	}

	entries, known := index.Entries[chart.Name]
	if !known {
		return "", fmt.Errorf("chart %q not in index", chart.Name)
	}

	for _, entry := range entries {
		if entry.Version == chart.Version {
			return entry.AppVersion, nil
		}
	}

	// The pin does not exist upstream at all, which is worse than a stale app
	// version and worth saying plainly.
	return "", fmt.Errorf("version %s not in index", chart.Version)
}

// outdated compares every pin against its repository index.
//
// It reads index.yaml directly rather than shelling out to `helm repo add` and
// `helm search`, which would mutate the operator's Helm configuration as a side
// effect of a read-only report.
func outdated() error {
	ctx, cancel := context.WithTimeout(context.Background(), indexTimeout)
	defer cancel()

	return reportOutdated(ctx, &http.Client{Timeout: indexRequestTimeout}, os.Stdout, os.Stderr, registryPins())
}

// registryPins is every chart in the registry, in key order.
func registryPins() []pin {
	keys := charts.Keys()
	pins := make([]pin, 0, len(keys))

	for _, key := range keys {
		pins = append(pins, pin{Key: key, Chart: charts.MustGet(key)})
	}

	return pins
}

// pin is one chart the report checks, under the key it is listed by.
type pin struct {
	Key   string
	Chart charts.Chart
}

// reportOutdated is outdated with its inputs passed in, so a test can run it
// against a local repository.
func reportOutdated(ctx context.Context, client *http.Client, stdout, stderr io.Writer, pins []pin) error {
	out := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(out, "CHART\tPINNED\tLATEST\tSTATUS")

	stale := 0

	// The charts that were not compared. Each still gets its row, and the
	// report then fails: a `?` in a table that exits zero reads as "nothing
	// outdated" when the truth is "nothing checked".
	var unchecked []string

	for _, p := range pins {
		key, chart := p.Key, p.Chart

		latest, err := latestInRepo(ctx, client, chart)
		if err != nil {
			fmt.Fprintf(out, "%s\t%s\t?\t%v\n", key, chart.Version, err)

			unchecked = append(unchecked, key)

			continue
		}

		pinned, err := ParseVersion(chart.Version)
		if err != nil {
			fmt.Fprintf(out, "%s\t%s\t%s\tunparseable pin\n", key, chart.Version, latest)

			unchecked = append(unchecked, key)

			continue
		}

		status := "current"
		if pinned.Compare(latest) < 0 {
			status = "OUTDATED"
			stale++
		}

		fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", key, pinned, latest, status)
	}

	if err := out.Flush(); err != nil {
		return err
	}

	if stale > 0 {
		// Report, do not fail. An upgrade is a decision with a changelog to
		// read, and a gate that fails on every upstream release is a gate
		// people switch off.
		fmt.Fprintf(stderr, "\n%d chart(s) behind upstream — read the changelogs before bumping\n", stale)
	}

	if len(unchecked) > 0 {
		return fmt.Errorf("%d of %d chart(s) could not be checked against upstream: %s",
			len(unchecked), len(pins), strings.Join(unchecked, ", "))
	}

	return nil
}

// repoIndex is the subset of a Helm repository index this tool reads.
//
// Decoded here rather than through Helm's own IndexFile, and that is a
// measured choice: helm.sh/helm/v4/pkg/repo/v1 links about 700 packages —
// the OCI registry client, the WASM plugin runtime, cli-runtime and
// client-go — to read two fields, and it loads an index only from a file on
// disk. The two fields are the stable part of the format; anything else in
// an entry is ignored, so a field Helm adds cannot break this.
type repoIndex struct {
	Entries map[string][]indexEntry `json:"entries"`
}

type indexEntry struct {
	Version    string `json:"version"`
	AppVersion string `json:"appVersion"`
}

// fetchIndex reads a repository's index.yaml.
//
// Directly rather than through `helm repo add` and `helm search`, which would
// mutate the operator's Helm configuration as a side effect of a read.
func fetchIndex(ctx context.Context, client *http.Client, repo string) (*repoIndex, error) {
	return fetchIndexWithin(ctx, client, repo, maxIndexBytes)
}

// maxIndexBytes bounds what an index may take. Some repository indexes are
// genuinely large; the limit is a guard against a misbehaving endpoint, not
// against a big project.
const maxIndexBytes = 64 << 20

// fetchIndexWithin is fetchIndex with its size limit passed in, so a test can
// reach the limit without serving 64 MiB.
func fetchIndexWithin(ctx context.Context, client *http.Client, repo string, limit int64) (*repoIndex, error) {
	url := repo
	if url[len(url)-1] != '/' {
		url += "/"
	}

	url += "index.yaml"

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch index: %w", err)
	}
	// The body is read below and this only releases the connection. A Close
	// error here would say nothing about whether the read succeeded, which
	// is the only question this function answers.
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("index returned %s", response.Status)
	}

	// One byte past the limit, so a body that reaches it is told apart from
	// one that fits exactly. Cut at the limit, an index is still YAML — just
	// one missing its newest entries — and would report a stale pin current.
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read index: %w", err)
	}

	if int64(len(body)) > limit {
		return nil, fmt.Errorf("index at %s is larger than %d bytes, so it was not read whole", url, limit)
	}

	var index repoIndex

	if err := yaml.Unmarshal(body, &index); err != nil {
		return nil, fmt.Errorf("parse index: %w", err)
	}

	return &index, nil
}

func latestInRepo(ctx context.Context, client *http.Client, chart charts.Chart) (*semver.Version, error) {
	index, err := fetchIndex(ctx, client, chart.Repo)
	if err != nil {
		return nil, err
	}

	entries, known := index.Entries[chart.Name]
	if !known {
		return nil, fmt.Errorf("chart %q not in index", chart.Name)
	}

	versions := make([]string, 0, len(entries))
	for _, entry := range entries {
		versions = append(versions, entry.Version)
	}

	latest, found := LatestStable(versions)
	if !found {
		return nil, fmt.Errorf("no stable version in index")
	}

	return latest, nil
}
