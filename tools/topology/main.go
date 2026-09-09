// Command topology validates every committed cluster topology file.
//
// It exists so that a malformed cluster description fails in CI, in a second,
// instead of at `pulumi up`, after an operator has already set up credentials
// and waited for a preview. The validation is the same code the Pulumi program
// runs — pkg/hetzner.LoadTopology — so the check cannot drift from the thing
// it is checking.
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/hetzner"
)

func main() {
	dirs := os.Args[1:]
	if len(dirs) == 0 {
		dirs = []string{"infra/cluster"}
	}

	failures, err := Validate(dirs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	if len(failures) > 0 {
		for _, failure := range failures {
			fmt.Fprintln(os.Stderr, failure)
		}

		os.Exit(1)
	}
}

// ErrNoTopologies is returned when a directory holds no cluster files at all.
//
// It is an error rather than a pass: a validator that silently succeeds
// because it found nothing to validate is worse than no validator, because it
// reports green while checking nothing.
var ErrNoTopologies = errors.New("no cluster.<stack>.yaml files found")

// Validate loads every cluster.<stack>.yaml under the given directories and
// returns one message per invalid file.
func Validate(dirs []string) ([]string, error) {
	var (
		failures []string
		checked  int
	)

	for _, dir := range dirs {
		paths, err := topologyFiles(dir)
		if err != nil {
			return nil, err
		}

		for _, path := range paths {
			checked++

			if _, err := hetzner.LoadTopology(path); err != nil {
				failures = append(failures, fmt.Sprintf("%s:\n%s", path, indent(err.Error())))

				continue
			}

			fmt.Printf("ok  %s\n", path)
		}
	}

	if checked == 0 {
		return nil, fmt.Errorf("%w under %s", ErrNoTopologies, strings.Join(dirs, ", "))
	}

	return failures, nil
}

// topologyFiles lists the committed topology files in a directory, sorted so
// output is stable between runs.
func topologyFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("directory %q does not exist", dir)
		}

		return nil, err
	}

	var paths []string

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !IsTopologyFile(name) {
			continue
		}

		paths = append(paths, filepath.Join(dir, name))
	}

	sort.Strings(paths)

	return paths, nil
}

// IsTopologyFile reports whether a filename is a committed cluster topology.
//
// The shape matters: infra/cluster/main.go derives the path from the stack
// name, so a file that does not follow it is never loaded by anything and
// would sit in the repository looking like configuration that is in effect.
func IsTopologyFile(name string) bool {
	if !strings.HasSuffix(name, ".yaml") {
		return false
	}

	// Strip the extension BEFORE the prefix: trimming the other way round
	// turns "cluster.yaml" — which names no stack — into the stack "yaml".
	stack, found := strings.CutPrefix(strings.TrimSuffix(name, ".yaml"), "cluster.")

	return found && stack != "" && !strings.Contains(stack, ".")
}

func indent(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, line := range lines {
		lines[i] = "    " + line
	}

	return strings.Join(lines, "\n")
}
