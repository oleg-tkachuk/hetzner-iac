// Command topology validates every committed cluster topology file.
//
// It exists so that a malformed cluster description fails in CI, in a second,
// instead of at `pulumi up`, after an operator has already set up credentials
// and waited for a preview. The validation is the same code the Pulumi program
// runs — internal/pkg/clusterspec.LoadTopology — so the check cannot drift from the thing
// it is checking.
package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
)

// Exit codes: a topology that does not load fails a run the same way an error
// does, since both mean CI must stop.
const (
	exitValid   = 0
	exitInvalid = 1
)

func main() {
	invalid, err := run(os.Args[1:], os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	os.Exit(exitCode(invalid))
}

// exitCode is non-zero when any topology failed to load.
func exitCode(invalid bool) int {
	if invalid {
		return exitInvalid
	}

	return exitValid
}

// getCommand and getArgs are the `get <field> <file>` form.
const (
	getCommand = "get"
	getArgs    = 3
)

// defaultDir is where the committed topologies live.
const defaultDir = "infra/cluster"

// run validates the topologies under the given directories, or with
// `get <field> <file>` prints one value from a topology. invalid reports that
// at least one topology failed; err that the run could not complete.
func run(args []string, stdout, stderr io.Writer) (invalid bool, err error) {
	// `get <field> <file>` prints one value from a topology, for the tasks that
	// need it — which talosctl to install, which image to bake, which
	// Kubernetes version to upgrade to.
	//
	// It exists because the shell alternative does not work. `grep -A3 '^talos:'
	// | grep version: | head -1 | awk '{print $2}'` reads three lines after a
	// key and hopes the field is among them, so it returns the right answer
	// only while nobody writes a comment. Three of the four call sites this
	// replaced were returning an empty string: the Talos version in prod and
	// the Kubernetes version in both, silently, because the comments above
	// those fields had grown past the window.
	//
	// An awk program with an indentation state machine would fix today's four
	// and still be a hand-rolled YAML reader — it would break on a quoted
	// value, an anchor, or a nested key of the same name. This asks the parser
	// the cluster itself is built from.
	if len(args) == getArgs && args[0] == getCommand {
		command := [getArgs]string(args)

		return false, printField(command[1], command[2], stdout)
	}

	dirs := args
	if len(dirs) == 0 {
		dirs = []string{defaultDir}
	}

	failures, err := Validate(dirs, stdout)
	if err != nil {
		return false, err
	}

	for _, failure := range failures {
		// Discarded as fmt.Fprintln's always was: the exit code still says the
		// topology is invalid.
		_, _ = fmt.Fprintln(stderr, failure)
	}

	return len(failures) > 0, nil
}

// printField writes one topology field, or nothing when it cannot be read.
func printField(field, path string, stdout io.Writer) error {
	value, err := get(field, path)
	if err != nil {
		return err
	}

	// Discarded as fmt.Println's always was: nothing is left to report a
	// failed write to but the same stream.
	_, _ = fmt.Fprintln(stdout, value)

	return nil
}

// ErrNoTopologies is returned when a directory holds no cluster files at all.
//
// It is an error rather than a pass: a validator that silently succeeds
// because it found nothing to validate is worse than no validator, because it
// reports green while checking nothing.
var ErrNoTopologies = errors.New("no cluster.<stack>.yaml files found")

// Validate loads every cluster.<stack>.yaml under the given directories and
// returns one message per invalid file, writing a line to out for each valid
// one.
func Validate(dirs []string, out io.Writer) ([]string, error) {
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

			if _, err := clusterspec.LoadTopology(path); err != nil {
				failures = append(failures, fmt.Sprintf("%s:\n%s", path, indent(err.Error())))

				continue
			}

			// Progress only, discarded as fmt.Printf's always was.
			_, _ = fmt.Fprintf(out, "ok  %s\n", path)
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
		if _, ok := clusterspec.TopologyStack(name); entry.IsDir() || !ok {
			continue
		}

		paths = append(paths, filepath.Join(dir, name))
	}

	slices.Sort(paths)

	return paths, nil
}

func indent(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, line := range lines {
		lines[i] = "    " + line
	}

	return strings.Join(lines, "\n")
}

// talosVersion reports the Talos version a topology pins.
// fields are the values `get` can print. A table rather than a switch so the
// error message can list them, and so a test can assert every one resolves.
var fields = map[string]func(*clusterspec.Topology) string{
	"talos-version":      func(t *clusterspec.Topology) string { return t.Talos.Version },
	"talos-architecture": func(t *clusterspec.Topology) string { return t.Talos.Architecture },
	"kubernetes-version": func(t *clusterspec.Topology) string { return t.Kubernetes.Version },
	"cluster-name":       func(t *clusterspec.Topology) string { return t.Metadata.Name },
	"location":           func(t *clusterspec.Topology) string { return t.Placement.Location },
}

// get reads one field, and refuses to print an empty one.
//
// Refusing matters more than reading: a caller that substitutes an empty
// string into a URL or an image selector builds something that looks
// plausible and is wrong. Every field here is either required or defaulted, so
// empty means the topology is not what the caller thinks it is.
func get(field, path string) (string, error) {
	read, known := fields[field]
	if !known {
		return "", fmt.Errorf("unknown field %q; known fields are %s",
			field, strings.Join(fieldNames(), ", "))
	}

	topology, err := clusterspec.LoadTopology(path)
	if err != nil {
		return "", err
	}

	value := read(topology)
	if value == "" {
		return "", fmt.Errorf("%s is empty in %s", field, path)
	}

	return value, nil
}

func fieldNames() []string {
	return slices.Sorted(maps.Keys(fields))
}
