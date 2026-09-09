// Command alloyvalidate checks the collector configuration against Alloy
// itself.
//
// The unit tests in pkg/observability prove the configuration says what was
// intended. They cannot prove Alloy parses it — and Alloy's failure mode is
// the worst kind for a log collector: the DaemonSet starts, crash-loops, and
// logs stop arriving without anything else going red.
//
// Alloy is run from its container image rather than required in PATH, because
// the version that matters is the one the pinned chart deploys, not whatever
// happens to be installed. The image tag is derived from that chart's app
// version.
//
// Measured against Alloy v1.19.2, this catches syntax errors, references to
// components that are not declared, and unknown component types — each of
// which otherwise surfaces only on a running cluster.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/charts"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/observability"
)

// alloyImage returns the image this check runs: the application version the
// pinned `alloy` chart deploys.
//
// Derived rather than written down, because the version that matters is the
// one the cluster will run. A hard-coded tag would keep validating an Alloy
// nobody deploys the moment the chart is bumped.
func alloyImage() (string, error) {
	chart, err := charts.Get("alloy")
	if err != nil {
		return "", err
	}

	if chart.AppVersion == "" {
		return "", fmt.Errorf("the alloy chart entry has no AppVersion, so there is no image tag to validate against")
	}

	return "grafana/alloy:" + chart.AppVersion, nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker is not installed, and Alloy is run from its image: %w", err)
	}

	file, err := writeConfig()
	if err != nil {
		return err
	}

	defer func() { _ = os.Remove(file) }()

	image, err := alloyImage()
	if err != nil {
		return err
	}

	if err := validate(ctx, file, image); err != nil {
		return err
	}

	fmt.Printf("ok    alloy config validates against %s\n", image)

	return nil
}

// writeConfig materialises the configuration the layer deploys.
func writeConfig() (string, error) {
	file, err := os.CreateTemp("", "alloy-*.alloy")
	if err != nil {
		return "", fmt.Errorf("temp file: %w", err)
	}

	if _, err := file.WriteString(observability.AlloyConfig()); err != nil {
		_ = file.Close()

		return "", fmt.Errorf("write config: %w", err)
	}

	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close config: %w", err)
	}

	// Readable inside the container, which runs as a different user.
	if err := os.Chmod(file.Name(), 0o644); err != nil { //nolint:gosec // must be world-readable for the container user
		return "", fmt.Errorf("chmod config: %w", err)
	}

	return file.Name(), nil
}

func validate(ctx context.Context, file, image string) error {
	args := []string{
		"run", "--rm",
		"--volume", file + ":/cfg/config.alloy:ro",
		"--entrypoint", "/bin/alloy",
		image,
		"validate", "/cfg/config.alloy",
	}

	// #nosec G204 -- a fixed argv; the only variable is a temp path this
	// program just created. Nothing reaches a shell.
	output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err == nil {
		return nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Errorf("alloy rejected the configuration:\n%s", indent(string(output)))
	}

	return fmt.Errorf("running %s: %w", image, err)
}

func indent(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, line := range lines {
		lines[i] = "      " + line
	}

	return strings.Join(lines, "\n")
}
