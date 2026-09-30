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
	command := "list"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}

	var err error

	switch command {
	case "list":
		err = list()
	case "outdated":
		err = outdated()
	case "render":
		err = renderAll()
	case "appversions":
		err = appversions()
	default:
		err = fmt.Errorf("unknown command %q; use list, outdated, render or appversions", command)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
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

// appversions checks that each entry's AppVersion is what the pinned chart
// actually ships.
//
// It exists because Renovate cannot maintain it. Renovate bumps Version — the
// helm datasource only knows chart versions — and AppVersion beside it then
// becomes a lie: a comment claiming a chart deploys something it does not.
// Mostly that is a misleading document — but it stops being only a document
// the moment anything derives a value from it, as a validation image tag was
// derived from alloy's while that chart was pinned here.
//
// A gate rather than a rewriter. An automated upgrade should stop for a human
// to read a changelog, and this is the check that makes it stop.
func appversions() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	client := &http.Client{Timeout: 30 * time.Second}

	out := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(out, "CHART\tVERSION\tPINNED APP\tUPSTREAM APP\tSTATUS")

	var wrong []string

	for _, key := range charts.Keys() {
		chart := charts.MustGet(key)

		upstream, err := appVersionInRepo(ctx, client, chart)
		if err != nil {
			fmt.Fprintf(out, "%s\t%s\t%s\t?\t%v\n", key, chart.Version, chart.AppVersion, err)
			wrong = append(wrong, key)

			continue
		}

		status, agrees := AppVersionStatus(chart.AppVersion, upstream)
		if !agrees {
			wrong = append(wrong, key)
		}

		fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\n",
			key, chart.Version, display(chart.AppVersion), display(upstream), status)
	}

	if err := out.Flush(); err != nil {
		return err
	}

	if len(wrong) > 0 {
		fmt.Fprintf(os.Stderr,
			"\n%d chart entr(ies) claim the wrong app version.\n"+
				"Fix internal/pkg/charts/registry.go so AppVersion is the UPSTREAM APP column above,\n"+
				"and update the `// app <version>` comment beside Version to match.\n",
			len(wrong))

		return fmt.Errorf("app version mismatch: %s", strings.Join(wrong, ", "))
	}

	return nil
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
	// A context so a hung registry cannot hang the whole report; the client
	// timeout alone does not cover a slow body.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	client := &http.Client{Timeout: 30 * time.Second}

	var pins []pin
	for _, key := range charts.Keys() {
		pins = append(pins, pin{Key: key, Chart: charts.MustGet(key)})
	}

	return reportOutdated(ctx, client, os.Stdout, os.Stderr, pins)
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
