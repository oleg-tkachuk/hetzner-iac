// Command golangci runs golangci-lint at the version CI pins.
//
// The failure it exists for, measured: a `go install`-built golangci-lint in
// ~/go/bin shadowed Homebrew's, the two were 2.12.2 and 2.13.2, and the older
// one reported eight goconst findings on an untouched main that CI — which
// pins 2.13.2 — does not report. `go:lint` was red, the pull request was
// green, and the pre-push hook refused to push work that was fine.
//
// So the version is not a detail of somebody's machine. It is the same pin CI
// installs, read from the same file, and this runs whichever binary answers to
// it: the repository's own copy first, then PATH.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"sigs.k8s.io/yaml"
)

// LocalBinary is where the lint:install task writes the pinned copy.
//
// Preferred over PATH, and that is the whole point: Homebrew carries one
// golangci-lint and a Go install carries another, and neither is necessarily
// the pinned one. An operator should not have to choose on their PATH.
const LocalBinary = "bin/golangci-lint"

// workflow holds the pin, and holds it once: CI installs the linter from this
// same value, with a Renovate annotation above it. A second copy here would be
// a second thing to bump.
const workflow = ".github/workflows/ci.yaml"

// pinKey is the environment entry that carries it.
const pinKey = "GOLANGCI_VERSION"

// reported matches what `golangci-lint --version` prints. The rest of that
// line is the build's own provenance and moves between releases.
var reported = regexp.MustCompile(`has version (\S+)`)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	root, err := repositoryRoot(ctx)
	if err != nil {
		return err
	}

	pinned, err := pin(filepath.Join(root, workflow))
	if err != nil {
		return err
	}

	binary, err := resolve(root)
	if err != nil {
		return err
	}

	installed, err := version(ctx, binary)
	if err != nil {
		return err
	}

	if installed != pinned {
		return fmt.Errorf("%s is %s and CI pins %s: a different linter reports different "+
			"findings, which is a red local run against a green pull request.\n"+
			"`task -t Taskfile.dev.yaml lint:install` writes the pinned one into %s, which this prefers over PATH",
			binary, installed, pinned, LocalBinary)
	}

	// Exec rather than capture: golangci-lint colours its own output and
	// reports progress, and a wrapper that buffers both makes a slow run look
	// like a hung one.
	// #nosec G204,G702 -- the binary is the pinned linter, resolved from bin/ or
	// PATH, and the arguments are the ones the operator typed after `lint`.
	// Running a linter with the caller's own flags is the whole purpose.
	linter := exec.CommandContext(ctx, binary, args...)
	linter.Stdin, linter.Stdout, linter.Stderr = os.Stdin, os.Stdout, os.Stderr
	linter.Dir = root

	if runErr := linter.Run(); runErr != nil {
		var exit *exec.ExitError
		if errors.As(runErr, &exit) {
			// The linter's own exit code, so a finding stays a finding rather
			// than becoming "the wrapper failed".
			os.Exit(exit.ExitCode())
		}

		return fmt.Errorf("run %s: %w", binary, runErr)
	}

	return nil
}

// repositoryRoot is where the pin and bin/ are, whichever directory this was
// called from.
func repositoryRoot(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("find the repository root: %w", err)
	}

	return strings.TrimSpace(string(out)), nil
}

// pin reads the version CI installs.
func pin(path string) (string, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- a constant path inside the repository
	if err != nil {
		return "", fmt.Errorf("read the pin from %s: %w", path, err)
	}

	return pinIn(raw)
}

// pinIn is pin without the filesystem, so a test can hold the parser to the
// shape of the workflow rather than to a fixture on disk.
//
// Parsed as yaml rather than matched with a regular expression: the value is a
// mapping entry, and a workflow that moves it or quotes it differently should
// still be read correctly.
func pinIn(raw []byte) (string, error) {
	var file struct {
		Env map[string]string `json:"env"`
	}

	if err := yaml.Unmarshal(raw, &file); err != nil {
		return "", fmt.Errorf("parse the workflow: %w", err)
	}

	pinned, ok := file.Env[pinKey]
	if !ok || pinned == "" {
		return "", fmt.Errorf("no %s in the workflow's env: nothing says which version CI runs", pinKey)
	}

	return normalise(pinned), nil
}

// normalise drops a leading v, because the pin carries one and the binary does
// not report one.
func normalise(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

// resolve is the repository's own copy first, then PATH.
func resolve(root string) (string, error) {
	local := filepath.Join(root, LocalBinary)
	if info, err := os.Stat(local); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
		return local, nil
	}

	found, err := exec.LookPath("golangci-lint")
	if err != nil {
		return "", fmt.Errorf("golangci-lint is not installed and %s does not exist either: "+
			"`task -t Taskfile.dev.yaml lint:install` writes the pinned one there: %w", LocalBinary, err)
	}

	return found, nil
}

// version asks the binary what it is.
func version(ctx context.Context, binary string) (string, error) {
	out, err := exec.CommandContext(ctx, binary, "--version").Output() // #nosec G204 -- the resolved linter
	if err != nil {
		return "", fmt.Errorf("ask %s for its version: %w", binary, err)
	}

	return versionIn(string(out))
}

// versionIn reads the version out of that output, separated so a test can
// cover the formats this has actually seen.
func versionIn(out string) (string, error) {
	found := reported.FindStringSubmatch(out)
	if found == nil {
		return "", fmt.Errorf("golangci-lint --version printed something this cannot read: %q",
			strings.TrimSpace(out))
	}

	return normalise(found[1]), nil
}
