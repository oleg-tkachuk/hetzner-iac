// Command stackstatus reports every tier's and every layer's Pulumi stack for
// one environment: the last operation and whether it succeeded, when and how
// long it ran, what it changed, how many resources the stack holds, which
// commit it was applied from — and the things a single `pulumi stack` never
// says, because they are about two stacks at once: whether every consumer
// references this environment's cluster, and whether the contract they were
// applied against is the one the cluster publishes.
//
//	stackstatus --stack dev infra/cluster infra/backup layers/10-node-platform …
//
// The projects are arguments, in the order to print them, so the layer order
// stays the one Taskfile.yaml's LAYERS holds rather than a second copy here.
//
// Everything is read through the Pulumi Automation API. The rendering and the
// judgements live in internal/pkg/stackstatus, which stays Pulumi-free.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/opthistory"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"golang.org/x/term"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterref"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/layer"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/stackstatus"
)

// Keys Pulumi records in an update's environment.
const (
	envGitHead     = "git.head"
	envGitHeadName = "git.headName"
	envGitDirty    = "git.dirty"
)

// branchRefPrefix is what Pulumi's git.headName carries before a branch name.
const branchRefPrefix = "refs/heads/"

// noColorEnv is the convention the taskfiles and internal/pkg/pulumilog honour.
const noColorEnv = "NO_COLOR"

// History paging: the first page, one entry — the last operation only.
const (
	historyPageSize = 1
	firstPage       = 1
)

// statusTimeout bounds the whole report. Each project is five CLI calls
// against the backend, and they run concurrently.
const statusTimeout = 3 * time.Minute

var (
	errNoStack    = errors.New("--stack is required")
	errNoProjects = errors.New("name at least one project directory")
)

func main() {
	stack := flag.String("stack", "", "the environment whose stacks to report, e.g. dev")

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

	if err := requireLogin(dirs); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
	defer cancel()

	header, err := readHeader(ctx, stack, dirs[0])
	if err != nil {
		return err
	}

	// Each project is read concurrently; the channels keep the argument order.
	pending := make([]chan reading, 0, len(dirs))

	for _, dir := range dirs {
		result := make(chan reading, 1)
		pending = append(pending, result)

		go func() { result <- readProject(ctx, dir, stack) }()
	}

	projects := make([]stackstatus.Project, 0, len(dirs))

	for _, result := range pending {
		read := <-result
		if read.project.IsCluster {
			header.Cluster = clusterOf(read, stack)
		}

		projects = append(projects, read.project)
	}

	return stackstatus.Render(os.Stdout, header, projects, time.Now(), painter())
}

// painter colours only a terminal, and never when NO_COLOR is set.
func painter() stackstatus.Painter {
	if _, off := os.LookupEnv(noColorEnv); off || !term.IsTerminal(int(os.Stdout.Fd())) {
		return stackstatus.Plain
	}

	return stackstatus.ANSI
}

func readHeader(ctx context.Context, stack, anyProject string) (stackstatus.Header, error) {
	header := stackstatus.Header{
		Stack:        stack,
		Head:         gitHead(ctx),
		WantContract: clusterref.ContractVersion,
	}

	// Backend and CLI version are the same for every project, since each
	// declares the same backend; any workspace answers them.
	workspace, err := auto.NewLocalWorkspace(ctx, auto.WorkDir(anyProject))
	if err != nil {
		return header, fmt.Errorf("open %s as a pulumi workspace: %w", anyProject, err)
	}

	who, err := workspace.WhoAmIDetails(ctx)
	if err != nil {
		return header, fmt.Errorf("ask the backend who this is: %w", err)
	}

	header.Backend, header.User = who.URL, who.User
	header.PulumiVersion = workspace.PulumiVersion()

	return header, nil
}

// gitHead is the working tree's commit. git has no Go library of its own, so
// this asks the CLI for single values, never for columns to parse.
func gitHead(ctx context.Context) stackstatus.Commit {
	git := func(args ...string) string {
		// #nosec G204 -- args are literals from this function, and
		// CommandContext takes an argument vector: there is no shell.
		out, err := exec.CommandContext(ctx, "git", args...).Output()
		if err != nil {
			return ""
		}

		return strings.TrimSpace(string(out))
	}

	return stackstatus.Commit{
		SHA:    git("rev-parse", "HEAD"),
		Branch: git("branch", "--show-current"),
		Dirty:  git("status", "--porcelain", "--untracked-files=no") != "",
	}
}

// reading is one project as read: the row, and what only the cluster tier's
// row is needed for — its outputs, its Pulumi project name and its console page.
type reading struct {
	project     stackstatus.Project
	outputs     map[string]any
	projectName string
	console     string
}

func readProject(ctx context.Context, dir, stack string) reading {
	read := reading{project: stackstatus.Project{
		Name:      filepath.Base(dir),
		IsCluster: filepath.Clean(dir) == clusterspec.ClusterDir,
	}}

	workspace, err := auto.NewLocalWorkspace(ctx, auto.WorkDir(dir))
	if err != nil {
		read.project.Err = err

		return read
	}

	if settings, settingsErr := workspace.ProjectSettings(ctx); settingsErr == nil {
		read.projectName = settings.Name.String()
	}

	summaries, err := workspace.ListStacks(ctx)
	if err != nil {
		read.project.Err = err

		return read
	}

	summary, current := findStack(summaries, stack)
	if summary == nil {
		return read
	}

	read.project.HasStack = true
	read.project.InProgress = summary.UpdateInProgress
	read.project.Resources = summary.ResourceCount
	read.console = summary.URL

	if stateErr := readState(ctx, workspace, summary.Name, &read); stateErr != nil {
		read.project.Err = stateErr

		return read
	}

	if !read.project.IsCluster {
		// A key that is declared with an empty default reads as empty; a
		// failure to read it says the same thing a row can act on — it is
		// not set to anything this report can check.
		if value, refErr := workspace.GetConfig(ctx, summary.Name, layer.ClusterStackRefKey); refErr == nil {
			read.project.ClusterRef = value.Value
		}
	}

	read.project.Last, err = lastUpdate(ctx, workspace, summary.Name, current)
	if err != nil {
		read.project.Err = err
	}

	return read
}

// findStack picks the environment's stack out of a listing, and names the
// stack the workspace currently has selected. A Pulumi Cloud listing names a
// stack outside the default organisation `<org>/<stack>`.
func findStack(summaries []auto.StackSummary, stack string) (*auto.StackSummary, string) {
	var found *auto.StackSummary

	current := ""

	for i := range summaries {
		name := summaries[i].Name
		if name == stack || strings.HasSuffix(name, "/"+stack) {
			found = &summaries[i]
		}

		if summaries[i].Current {
			current = name
		}
	}

	return found, current
}

// readState reads the checkpoint: what did not finish cleanly, and the
// outputs. Export rather than StackOutputs, because the checkpoint keeps
// secrets as ciphertext while StackOutputs decrypts every one of them — and
// this report prints none.
func readState(ctx context.Context, workspace auto.Workspace, stack string, read *reading) error {
	untyped, err := workspace.ExportStack(ctx, stack)
	if err != nil {
		return fmt.Errorf("export %s: %w", stack, err)
	}

	deployment, err := decodeDeployment(untyped)
	if err != nil {
		return err
	}

	read.project.State = stateOf(deployment)
	read.outputs = rootOutputs(deployment)
	read.project.Contract = intOutput(read.outputs, clusterref.OutputContractVersion)

	return nil
}

// decodeDeployment reads an exported checkpoint into the SDK's own type.
//
// The SDK's decoder, stack.DeserializeUntypedDeployment, lives in the engine
// module (github.com/pulumi/pulumi/pkg/v3) with the whole engine behind it;
// this reads the same apitype the decoder does, and refuses a schema version
// it was not written for rather than misreading one.
func decodeDeployment(untyped apitype.UntypedDeployment) (apitype.DeploymentV3, error) {
	var deployment apitype.DeploymentV3

	if untyped.Version != apitype.DeploymentSchemaVersionCurrent {
		return deployment, fmt.Errorf("checkpoint schema v%d, and this reads v%d",
			untyped.Version, apitype.DeploymentSchemaVersionCurrent)
	}

	if err := json.Unmarshal(untyped.Deployment, &deployment); err != nil {
		return deployment, fmt.Errorf("decode checkpoint: %w", err)
	}

	return deployment, nil
}

// stateOf counts what the checkpoint says did not finish cleanly.
func stateOf(deployment apitype.DeploymentV3) stackstatus.State {
	state := stackstatus.State{PendingOperations: len(deployment.PendingOperations)}

	for _, res := range deployment.Resources {
		if res.Delete {
			state.PendingDeletion++
		}

		if res.PendingReplacement {
			state.PendingReplacement++
		}

		if res.Taint {
			state.Tainted++
		}

		if len(res.InitErrors) > 0 {
			state.InitErrors++
		}
	}

	return state
}

// rootOutputs are the stack's exported outputs, as the root resource holds
// them. A secret stays a ciphertext envelope, which the readers below treat
// as absent.
func rootOutputs(deployment apitype.DeploymentV3) map[string]any {
	for _, res := range deployment.Resources {
		if res.Type == resource.RootStackType && res.Parent == "" {
			return res.Outputs
		}
	}

	return nil
}

// intOutput reads a numeric output; JSON numbers arrive as float64.
func intOutput(outputs map[string]any, key string) *int {
	value, ok := outputs[key].(float64)
	if !ok {
		return nil
	}

	n := int(value)

	return &n
}

// stringOutput reads a plain string output, and nothing for a secret.
func stringOutput(outputs map[string]any, key string) string {
	value, _ := outputs[key].(string)

	return value
}

// clusterOf is the header's account of the cluster, from its tier's row.
func clusterOf(read reading, stack string) stackstatus.Cluster {
	cluster := stackstatus.Cluster{
		Name:     stringOutput(read.outputs, clusterref.OutputClusterName),
		Location: stringOutput(read.outputs, clusterref.OutputLocation),
		Endpoint: stringOutput(read.outputs, clusterref.OutputEndpoint),
		Contract: read.project.Contract,
		Console:  read.console,
	}

	if read.projectName != "" {
		cluster.Stack = read.projectName + "/" + stack
	}

	return cluster
}

// lastUpdate reads the stack's last operation.
//
// History is the one read that needs a Stack rather than a workspace, and the
// SDK builds one only by selecting it — `pulumi stack select`, which rewrites
// the operator's own selection for this directory. So the selection found
// before is put back after. With nothing selected before there is nothing to
// put back, and the CLI has no way to unselect.
func lastUpdate(ctx context.Context, workspace auto.Workspace, stack, previous string) (*stackstatus.Update, error) {
	selected, err := auto.SelectStack(ctx, stack, workspace)
	if err != nil {
		return nil, fmt.Errorf("select %s: %w", stack, err)
	}

	history, err := selected.History(ctx, historyPageSize, firstPage, opthistory.ShowSecrets(false))

	if previous != "" && previous != stack {
		if restoreErr := workspace.SelectStack(ctx, previous); restoreErr != nil && err == nil {
			err = fmt.Errorf("reselect %s: %w", previous, restoreErr)
		}
	}

	if err != nil {
		return nil, fmt.Errorf("history of %s: %w", stack, err)
	}

	if len(history) == 0 {
		return nil, nil //nolint:nilnil // a stack that was created and never run has no last update
	}

	return toUpdate(history[0]), nil
}

// toUpdate maps the Automation API's summary onto the report's type.
func toUpdate(summary auto.UpdateSummary) *stackstatus.Update {
	update := &stackstatus.Update{
		Number: summary.Version,
		Kind:   summary.Kind,
		Result: result(apitype.UpdateResult(summary.Result)),
		Start:  parseTime(summary.StartTime),
		Commit: stackstatus.Commit{
			SHA:    summary.Environment[envGitHead],
			Branch: strings.TrimPrefix(summary.Environment[envGitHeadName], branchRefPrefix),
			Dirty:  summary.Environment[envGitDirty] == strconv.FormatBool(true),
		},
	}

	if summary.EndTime != nil {
		update.End = parseTime(*summary.EndTime)
	}

	if summary.ResourceChanges != nil {
		update.Changes = *summary.ResourceChanges
	}

	return update
}

func result(pulumiResult apitype.UpdateResult) stackstatus.Result {
	switch pulumiResult {
	case apitype.SucceededResult:
		return stackstatus.ResultSucceeded
	case apitype.FailedResult:
		return stackstatus.ResultFailed
	case apitype.InProgressResult:
		return stackstatus.ResultInProgress
	case apitype.NotStartedResult:
		return stackstatus.ResultNotStarted
	default:
		return stackstatus.Result(pulumiResult)
	}
}

// parseTime reads Pulumi's RFC 3339 timestamps; an unreadable one is zero.
func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}

	return t
}
