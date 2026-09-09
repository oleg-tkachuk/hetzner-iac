package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"text/tabwriter"
	"time"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/charts"

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
	default:
		err = fmt.Errorf("unknown command %q; use list or outdated", command)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
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

// outdated compares every pin against its repository index.
//
// It reads index.yaml directly rather than shelling out to `helm repo add` and
// `helm search`, which would mutate the operator's Helm configuration as a side
// effect of a read-only report.
func outdated() error {
	out := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(out, "CHART\tPINNED\tLATEST\tSTATUS")

	// A context so a hung registry cannot hang the whole report; the client
	// timeout alone does not cover a slow body.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	client := &http.Client{Timeout: 30 * time.Second}
	stale := 0

	for _, key := range charts.Keys() {
		chart := charts.MustGet(key)

		latest, err := latestInRepo(ctx, client, chart)
		if err != nil {
			fmt.Fprintf(out, "%s\t%s\t?\t%v\n", key, chart.Version, err)

			continue
		}

		pinned, err := ParseVersion(chart.Version)
		if err != nil {
			fmt.Fprintf(out, "%s\t%s\t%s\tunparseable pin\n", key, chart.Version, latest)

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
		fmt.Fprintf(os.Stderr, "\n%d chart(s) behind upstream — read the changelogs before bumping\n", stale)
	}

	return nil
}

// repoIndex is the subset of a Helm repository index this tool reads.
type repoIndex struct {
	Entries map[string][]struct {
		Version string `json:"version"`
	} `json:"entries"`
}

func latestInRepo(ctx context.Context, client *http.Client, chart charts.Chart) (Version, error) {
	url := chart.Repo
	if url[len(url)-1] != '/' {
		url += "/"
	}

	url += "index.yaml"

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Version{}, fmt.Errorf("build request: %w", err)
	}

	response, err := client.Do(request)
	if err != nil {
		return Version{}, fmt.Errorf("fetch index: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return Version{}, fmt.Errorf("index returned %s", response.Status)
	}

	// Some repository indexes are genuinely large; the limit is a guard
	// against a misbehaving endpoint, not against a big project.
	const maxIndexBytes = 64 << 20

	body, err := io.ReadAll(io.LimitReader(response.Body, maxIndexBytes))
	if err != nil {
		return Version{}, fmt.Errorf("read index: %w", err)
	}

	var index repoIndex

	if err := yaml.Unmarshal(body, &index); err != nil {
		return Version{}, fmt.Errorf("parse index: %w", err)
	}

	entries, known := index.Entries[chart.Name]
	if !known {
		return Version{}, fmt.Errorf("chart %q not in index", chart.Name)
	}

	versions := make([]string, 0, len(entries))
	for _, entry := range entries {
		versions = append(versions, entry.Version)
	}

	latest, found := LatestStable(versions)
	if !found {
		return Version{}, fmt.Errorf("no stable version in index")
	}

	return latest, nil
}
