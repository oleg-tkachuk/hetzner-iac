// Command stack lists the cluster tier's environments.
//
// Two independent places answer half the question each: the Pulumi backend
// knows which stacks exist, and the committed `cluster.<stack>.yaml` files
// describe the clusters they build. The interesting rows are the ones missing
// one — a topology with no stack is an environment nobody has created, and a
// stack with no topology is one whose description is not in this working copy.
//
// The generic half of what this used to do — exists, ensure and ref — is
// github.com/oleg-tkachuk/pulumi-kit/cmd/stack, which the taskfiles call
// directly. What stayed is the part that reads this repository's topology.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optlist"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/pulumilogin"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/stackstatus"
)

func main() {
	if err := run(context.Background(), os.Args[1:], liveDeps()); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// stackLister is the part of a Pulumi workspace list reads.
type stackLister interface {
	ListStacks(ctx context.Context, opts ...optlist.Option) ([]auto.StackSummary, error)
}

// deps are what run reads from outside the process, so a test can stand in
// for each one.
type deps struct {
	requireLogin func(dirs []string) error
	open         func(ctx context.Context, dir string) (stackLister, error)
	out          io.Writer
}

// liveDeps are the real backend and terminal.
func liveDeps() deps {
	return deps{requireLogin: pulumilogin.Require, open: openWorkspace, out: os.Stdout}
}

// openWorkspace opens dir as a workspace of the backend this machine is
// logged in to.
func openWorkspace(ctx context.Context, dir string) (stackLister, error) {
	return auto.NewLocalWorkspace(ctx, auto.WorkDir(dir))
}

// Usage is what a wrong argument list gets.
const Usage = "usage: stack list <cluster-dir>"

func run(ctx context.Context, args []string, d deps) error {
	if len(args) != 2 || args[0] != "list" {
		return errors.New(Usage)
	}

	return list(ctx, args[1], d)
}

// listTimeout bounds the one backend read: listing the project's stacks.
const listTimeout = time.Minute

// Environment is one stack of the cluster tier and what is known about it
// from two independent places: the Pulumi backend, and the committed topology
// file that describes the cluster it builds.
//
// Both halves are optional and the interesting rows are the ones missing one.
// A topology with no stack is an environment nobody has created yet; a stack
// with no topology is one whose description is not in this working copy —
// `cluster.<stack>.yaml` is gitignored, so that is what a fresh clone sees.
type Environment struct {
	Name string
	// Topology is nil when there is no file for this stack, or when the file
	// is there and unreadable — Problem then says which.
	Topology *clusterspec.Topology
	Problem  error
	Present  bool
	// InBackend is false for an environment described by a file that no
	// Pulumi stack exists for yet.
	InBackend bool
	Updated   string
	Resources int
}

// topologyFile is a loaded topology, or the reason it could not be loaded.
// Failing to read one file does not hide the other environments.
type topologyFile struct {
	Topology *clusterspec.Topology
	Err      error
}

// listRow is the table's shape, wide enough for the values Hetzner and Talos
// actually produce: a stack name, a location, an architecture, two versions,
// and a node summary carrying a pool.
const listRow = "%-10s %-8s %-8s %-4s %-10s %-10s %-20s %9s %s"

// missing is what a cell says when the value cannot be known rather than
// being empty: no topology to read it from, or no stack to have counted it.
const missing = "-"

// list prints every environment the cluster tier has, from the backend and
// from the committed topologies at once.
func list(ctx context.Context, dir string, d deps) error {
	listing, err := stacks(ctx, dir, d)
	if err != nil {
		return err
	}

	files, err := topologies(dir)
	if err != nil {
		return err
	}

	_, err = io.WriteString(d.out, table(merge(listing, files)))

	return err
}

// stacks lists the project's stacks in dir through the automation API, after
// refusing a backend this machine holds no credential for.
func stacks(ctx context.Context, dir string, d deps) ([]auto.StackSummary, error) {
	if err := d.requireLogin([]string{dir}); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()

	workspace, err := d.open(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("open %s as a pulumi workspace: %w", dir, err)
	}

	summaries, err := workspace.ListStacks(ctx)
	if err != nil {
		return nil, fmt.Errorf("list stacks in %s: %w", dir, err)
	}

	return summaries, nil
}

// topologies loads every committed topology in dir, keyed by the stack its
// name carries.
//
// The example is skipped: it is a template with a documentation CIDR in it,
// not an environment anybody applies.
func topologies(dir string) (map[string]topologyFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("look for topologies in %s: %w", dir, err)
	}

	files := map[string]topologyFile{}

	for _, entry := range entries {
		name, ok := clusterspec.TopologyStack(entry.Name())
		if entry.IsDir() || !ok || name == clusterspec.ExampleTopologyStack {
			continue
		}

		topology, loadErr := clusterspec.LoadTopology(filepath.Join(dir, entry.Name()))
		files[name] = topologyFile{Topology: topology, Err: loadErr}
	}

	return files, nil
}

// merge is the union of the two sources, sorted by name, separated from both
// so it can be tested without a Pulumi backend or a filesystem.
func merge(listing []auto.StackSummary, files map[string]topologyFile) []Environment {
	found := map[string]*Environment{}

	for _, summary := range listing {
		found[summary.Name] = &Environment{
			Name:      summary.Name,
			InBackend: true,
			Updated:   summary.LastUpdate,
			Resources: resourceCount(summary),
		}
	}

	for name, file := range files {
		environment := listedAs(found, name)
		if environment == nil {
			environment = &Environment{Name: name}
		}

		// Filed under the topology's name, which is the one every task here
		// is given: Pulumi resolves `dev` to `acme/dev` by itself.
		delete(found, environment.Name)
		environment.Name = name
		found[name] = environment

		environment.Present = true
		environment.Topology = file.Topology
		environment.Problem = file.Err
	}

	names := make([]string, 0, len(found))
	for name := range found {
		names = append(names, name)
	}

	sort.Strings(names)

	environments := make([]Environment, 0, len(names))
	for _, name := range names {
		environments = append(environments, *found[name])
	}

	return environments
}

// resourceCount is the listing's count, or zero where the backend leaves it
// out — a stack that has never been updated, which holds none.
func resourceCount(summary auto.StackSummary) int {
	if summary.ResourceCount == nil {
		return 0
	}

	return *summary.ResourceCount
}

// listedAs finds the listed stack a topology's name refers to. Pulumi Cloud
// lists `acme/dev` for the stack every command here calls `dev`, so an exact
// lookup split one environment into two rows.
func listedAs(found map[string]*Environment, name string) *Environment {
	if environment, listed := found[name]; listed {
		return environment
	}

	for listed, environment := range found {
		if environment.InBackend && stackstatus.SameStack(listed, name) {
			return environment
		}
	}

	return nil
}

// table renders the environments, header included.
func table(environments []Environment) string {
	var out strings.Builder

	fmt.Fprintf(&out, listRow+"\n", "STACK", "TOPOLOGY", "LOCATION", "ARCH",
		"TALOS", "KUBERNETES", "NODES", "RESOURCES", "UPDATED")

	for _, environment := range environments {
		location, arch, talos, kubernetes, nodes := missing, missing, missing, missing, missing

		if topology := environment.Topology; topology != nil {
			location = topology.Placement.Location
			arch = topology.Talos.Architecture
			talos = topology.Talos.Version
			kubernetes = topology.Kubernetes.Version
			nodes = summary(topology)
		}

		resources, updated := missing, missing

		if environment.InBackend {
			resources = strconv.Itoa(environment.Resources)
			updated = when(environment.Updated)
		}

		fmt.Fprintf(&out, listRow+"\n", environment.Name, state(environment),
			location, arch, talos, kubernetes, nodes, resources, updated)
	}

	return out.String()
}

// state says which of the two halves this environment has, because it is what
// explains the empty cells beside it.
func state(environment Environment) string {
	switch {
	case environment.Problem != nil:
		return "invalid"
	case !environment.Present:
		return "missing"
	case !environment.InBackend:
		return "no stack"
	default:
		return "yes"
	}
}

// summary is the node shape in one cell: the control plane, and each worker
// pool after it.
//
// The server types are the EFFECTIVE ones. A sparse topology leaves them
// empty and takes the default for its architecture, so printing the field
// verbatim would show a blank for the commonest case — and LoadTopology has
// already defaulted them, which is the whole reason this reads the loaded
// topology rather than the file.
func summary(topology *clusterspec.Topology) string {
	parts := []string{fmt.Sprintf("%d x %s",
		topology.ControlPlane.Count, topology.ControlPlane.ServerType)}

	for _, pool := range topology.WorkerPools {
		parts = append(parts, fmt.Sprintf("%d x %s", pool.Count, pool.ServerType))
	}

	return strings.Join(parts, ", ")
}

// when shortens Pulumi's timestamp to the minute, which is the precision an
// operator reads it at.
//
// An empty value means the stack exists and has never been updated, which is
// not the same as a stack that is not there — the caller has already decided
// which of those it is looking at.
func when(stamp string) string {
	if stamp == "" {
		return "never"
	}

	moment, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		// Pulumi's own value, unparsed rather than dropped: a format change
		// upstream should show up as an odd-looking column, not as a blank.
		return stamp
	}

	return moment.UTC().Format("2006-01-02 15:04Z")
}
