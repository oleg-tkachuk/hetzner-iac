// Command stackdrift reports, for one environment, every resource the cloud
// no longer agrees with: the stacks' refresh previews, read through the
// Pulumi Automation API. Nothing is written — a refresh preview reads the
// cloud and leaves the state as it was.
//
//	stackdrift --stack dev infra/cluster infra/backup layers/10-node-platform …
//
// It exits 1 when anything drifted or a stack could not be read, so a
// scheduled run can fail on it. The report itself lives in
// internal/pkg/stackdrift, which stays Pulumi-free.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/events"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optrefresh"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"golang.org/x/term"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/pulumilogin"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/stackdrift"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/stackstatus"
)

// noColorEnv is the convention the taskfiles and internal/pkg/pulumilog honour.
const noColorEnv = "NO_COLOR"

// pulumiPackage is the package of the engine's own resource types.
const pulumiPackage = "pulumi"

// driftTimeout bounds the whole report. A refresh preview reads every
// resource from its provider, and the stacks run concurrently.
const driftTimeout = 10 * time.Minute

var (
	errNoStack    = errors.New("--stack is required")
	errNoProjects = errors.New("name at least one project directory")
	errDrifted    = errors.New("the cloud differs from the state, or a stack could not be read")
)

func main() {
	stack := flag.String("stack", "", "the environment whose stacks to check, e.g. dev")

	flag.Parse()

	if err := run(*stack, flag.Args()); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(stack string, dirs []string) error {
	switch {
	case stack == "":
		return errNoStack
	case len(dirs) == 0:
		return errNoProjects
	}

	if err := pulumilogin.Require(dirs); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), driftTimeout)
	defer cancel()

	// Concurrently; the channels keep the argument order.
	pending := make([]chan stackdrift.Project, 0, len(dirs))

	for _, dir := range dirs {
		result := make(chan stackdrift.Project, 1)
		pending = append(pending, result)

		go func() { result <- readProject(ctx, dir, stack) }()
	}

	projects := make([]stackdrift.Project, 0, len(dirs))
	for _, result := range pending {
		projects = append(projects, <-result)
	}

	if err := stackdrift.Render(os.Stdout, stack, projects, painter()); err != nil {
		return err
	}

	if stackdrift.Drifted(projects) {
		return errDrifted
	}

	return nil
}

func readProject(ctx context.Context, dir, stack string) stackdrift.Project {
	project := stackdrift.Project{Name: filepath.Base(dir)}

	workspace, err := auto.NewLocalWorkspace(ctx, auto.WorkDir(dir))
	if err != nil {
		project.Err = err

		return project
	}

	summaries, err := workspace.ListStacks(ctx)
	if err != nil {
		project.Err = err

		return project
	}

	name, previous := findStack(summaries, stack)
	if name == "" {
		return project
	}

	project.HasStack = true
	project.Changes, project.Err = previewRefresh(ctx, workspace, name, previous)

	return project
}

// findStack returns the listed name of the wanted stack and the one currently
// selected in the directory.
func findStack(summaries []auto.StackSummary, stack string) (found, current string) {
	for _, summary := range summaries {
		if stackstatus.SameStack(summary.Name, stack) {
			found = summary.Name
		}

		if summary.Current {
			current = summary.Name
		}
	}

	return found, current
}

// previewRefresh runs the stack's refresh preview and collects what changed.
//
// The SDK builds a Stack only by selecting it — `pulumi stack select`, which
// rewrites the operator's selection for this directory — so the selection
// found before is put back after, as tools/stackstatus does.
func previewRefresh(ctx context.Context, workspace auto.Workspace, stack, previous string) ([]stackdrift.Change, error) {
	selected, err := auto.SelectStack(ctx, stack, workspace)
	if err != nil {
		return nil, fmt.Errorf("select %s: %w", stack, err)
	}

	stream := make(chan events.EngineEvent)
	collected := make(chan []stackdrift.Change, 1)

	go func() { collected <- collect(stream) }()

	_, err = selected.PreviewRefresh(ctx, optrefresh.EventStreams(stream))

	// The SDK closes the stream once its log is drained, error or not.
	changes := <-collected

	if previous != "" && previous != stack {
		if restoreErr := workspace.SelectStack(ctx, previous); restoreErr != nil && err == nil {
			err = fmt.Errorf("reselect %s: %w", previous, restoreErr)
		}
	}

	if err != nil {
		return nil, fmt.Errorf("refresh preview of %s: %w", stack, err)
	}

	return changes, nil
}

// collect reads the engine events of a refresh preview until the stream
// closes. The change is on the outputs event: its op is what the refresh
// would do, and its detailed diff names the fields.
func collect(stream <-chan events.EngineEvent) []stackdrift.Change {
	var changes []stackdrift.Change

	for event := range stream {
		if event.ResOutputsEvent == nil {
			continue
		}

		if change, ok := toChange(event.ResOutputsEvent.Metadata); ok {
			changes = append(changes, change)
		}
	}

	return changes
}

// driftOps maps the refresh operations that mean the cloud moved onto the
// report's. Everything else — same, refresh, read — is a resource that agrees.
func driftOps() map[apitype.OpType]string {
	return map[apitype.OpType]string{
		apitype.OpUpdate: stackdrift.OpUpdate,
		apitype.OpDelete: stackdrift.OpDelete,
	}
}

// toChange keeps the operations that mean the cloud moved.
func toChange(step apitype.StepEventMetadata) (stackdrift.Change, bool) {
	op, ok := driftOps()[step.Op]
	if !ok {
		return stackdrift.Change{}, false
	}

	urn := resource.URN(step.URN)

	// The engine's own resources — the stack, its providers, a StackReference
	// — read no cloud. A StackReference "changes" when the stack it reads was
	// applied again, which platform:status reports as a contract question.
	if urn.Type().Package() == pulumiPackage {
		return stackdrift.Change{}, false
	}

	return stackdrift.Change{
		Type:   string(urn.Type()),
		Name:   urn.Name(),
		Op:     op,
		Fields: slices.Sorted(maps.Keys(step.DetailedDiff)),
	}, true
}

func painter() stackstatus.Painter {
	if _, off := os.LookupEnv(noColorEnv); off || !term.IsTerminal(int(os.Stdout.Fd())) {
		return stackstatus.Plain
	}

	return stackstatus.ANSI
}
