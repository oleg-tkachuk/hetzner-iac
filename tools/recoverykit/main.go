// Command recoverykit prints the parts of a cluster that live nowhere but
// Pulumi's state, in one document meant for a password store.
//
// It exists because the backups this repository already takes are not
// self-sufficient, and the dependency runs the wrong way:
//
//   - an etcd snapshot on the Storage Box is encrypted by restic
//   - restic's repository password is a random.NewRandomPassword generated
//     into infra/backup's state, and re-applying that project generates a
//     NEW one, which does not open the existing repository
//   - a restored snapshot is useless without the Talos secrets bundle, which
//     is in the cluster tier's state
//
// So losing the state turns every snapshot into ciphertext nobody can open.
// What this prints is the set that breaks that circle: with it, the snapshots
// are readable and a cluster can be rebuilt without reaching Pulumi at all.
//
// It is deliberately NOT a state backup. `pulumi stack export` covers the
// resource graph and is the answer to a deleted stack; this covers the case
// where the backend itself is out of reach, which an export encrypted by that
// backend's own key does not.
//
// stdout, and only stdout — the same discipline as tools/secrets and
// tools/token, and it refuses a terminal for the same reason: the one thing
// worse than no copy of a certificate authority is one in scrollback.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optlist"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/pulumilogin"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/secretout"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/stackstatus"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/talossecrets"
)

// stackName is what may be joined onto a path. Pulumi's own stack names allow
// letters, digits, hyphens, underscores and periods; a period is permitted
// here but a leading one is not, which is what keeps `..` out.
var stackName = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*$`)

// stackNameExtra names the rest for the error message, so the message and the
// pattern cannot drift into disagreeing about what is allowed.
const stackNameExtra = "hyphens, underscores and periods, and not a leading period"

// timeout covers every Pulumi read this makes: the cluster tier's export, the
// backup tier's outputs — which the automation API reads twice, once with
// secrets — and, when those fail, its stack listing.
const timeout = 120 * time.Second

// BackupDir is the layer whose outputs hold the backup credentials. Every
// output of it is taken rather than a named few: they are all generated, none
// is ever typed, and a list of names here would be a third copy of them.
const BackupDir = "infra/backup"

// TopologyDir is where the committed-shaped-but-gitignored topology lives:
// the cluster tier's directory, which clusterspec owns.
const TopologyDir = clusterspec.ClusterDir

func main() {
	if err := run(context.Background(), livePulumi(), os.Stdout, os.Args[1:], secretout.IsTerminal()); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// pulumiReader is everything this command reads from Pulumi, behind an
// interface so a test can answer for a backend: whether a stack is absent or
// could not be read is decided here, and it is the part worth testing.
type pulumiReader interface {
	// Bundle is the stack's Talos secrets bundle.
	Bundle(ctx context.Context, stack string) ([]byte, error)
	// Outputs is every output of the stack in dir, secrets decrypted, as JSON.
	Outputs(ctx context.Context, dir, stack string) ([]byte, error)
	// Stacks names every stack of the project in dir, as Pulumi lists them.
	Stacks(ctx context.Context, dir string) ([]string, error)
}

// pulumiWorkspace reads through Pulumi's automation API, one local workspace
// per project directory.
type pulumiWorkspace struct {
	requireLogin func(dirs []string) error
	newWorkspace func(ctx context.Context, dir string) (stackReader, error)
}

// stackReader is the part of a Pulumi workspace the kit reads.
type stackReader interface {
	ExportStack(ctx context.Context, stack string) (apitype.UntypedDeployment, error)
	StackOutputs(ctx context.Context, stack string) (auto.OutputMap, error)
	ListStacks(ctx context.Context, opts ...optlist.Option) ([]auto.StackSummary, error)
}

// livePulumi reads the backend this machine is logged in to.
func livePulumi() pulumiWorkspace {
	return pulumiWorkspace{requireLogin: pulumilogin.Require, newWorkspace: newLocalWorkspace}
}

// newLocalWorkspace opens dir as a local workspace.
func newLocalWorkspace(ctx context.Context, dir string) (stackReader, error) {
	return auto.NewLocalWorkspace(ctx, auto.WorkDir(dir))
}

// open is the project in dir as a workspace, after refusing a backend this
// machine holds no credential for. Checked here, at the first read, rather
// than in main: a terminal is refused before anything touches Pulumi.
func (p pulumiWorkspace) open(ctx context.Context, dir string) (stackReader, error) {
	if err := p.requireLogin([]string{dir}); err != nil {
		return nil, err
	}

	workspace, err := p.newWorkspace(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("open %s as a pulumi workspace: %w", dir, err)
	}

	return workspace, nil
}

func (p pulumiWorkspace) Bundle(ctx context.Context, stack string) ([]byte, error) {
	workspace, err := p.open(ctx, TopologyDir)
	if err != nil {
		return nil, err
	}

	// ExportStack always passes --show-secrets: without it the bundle would
	// be the ciphertext only this backend can read.
	exported, err := workspace.ExportStack(ctx, stack)
	if err != nil {
		return nil, fmt.Errorf("export %s in %s: %w", stack, TopologyDir, withoutCapturedOutput(err))
	}

	raw, err := json.Marshal(exported)
	if err != nil {
		return nil, fmt.Errorf("re-encode the export of %s: %w", stack, err)
	}

	return talossecrets.BundleFrom(raw, stack)
}

func (p pulumiWorkspace) Outputs(ctx context.Context, dir, stack string) ([]byte, error) {
	workspace, err := p.open(ctx, dir)
	if err != nil {
		return nil, err
	}

	outputs, err := workspace.StackOutputs(ctx, stack)
	if err != nil {
		return nil, fmt.Errorf("outputs of %s in %s: %w", stack, dir, withoutCapturedOutput(err))
	}

	return outputsJSON(outputs)
}

func (p pulumiWorkspace) Stacks(ctx context.Context, dir string) ([]string, error) {
	workspace, err := p.open(ctx, dir)
	if err != nil {
		return nil, err
	}

	summaries, err := workspace.ListStacks(ctx)
	if err != nil {
		return nil, fmt.Errorf("list stacks in %s: %w", dir, err)
	}

	return stackNames(summaries), nil
}

// stackNames is a listing reduced to the names, as the backend spells them:
// `acme/dev` stays qualified, and stackstatus.SameStack matches it to `dev`.
func stackNames(summaries []auto.StackSummary) []string {
	names := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		names = append(names, summary.Name)
	}

	return names
}

// outputsIndent is the indentation `pulumi stack output --json` prints with,
// kept so the stored kit reads as it did when this called the CLI.
const outputsIndent = "  "

// outputsJSON is every output's value as one JSON object, the document
// `pulumi stack output --json --show-secrets` prints.
//
// Secret outputs arrive already decrypted: the automation API reads them with
// --show-secrets and keeps only a flag saying they were secret, which is
// dropped here — the kit exists to hold the plaintext.
func outputsJSON(outputs auto.OutputMap) ([]byte, error) {
	values := make(map[string]any, len(outputs))
	for key, output := range outputs {
		values[key] = output.Value
	}

	var out bytes.Buffer

	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", outputsIndent)

	if err := encoder.Encode(values); err != nil {
		return nil, fmt.Errorf("encode the backup tier outputs: %w", err)
	}

	return out.Bytes(), nil
}

// withoutCapturedOutput keeps a decrypted read off stderr.
//
// The automation API's error carries the command's whole stdout. For a
// command that failed that is empty — the CLI prints its JSON only once the
// read succeeded — and the stderr beside it is the diagnosis worth keeping.
// For one that succeeded and could not be decoded, it is the decrypted export
// or the plaintext outputs, so only the SDK's own message is kept.
func withoutCapturedOutput(err error) error {
	if exited, ok := errors.AsType[*exec.ExitError](err); ok && exited != nil {
		return err
	}

	if inner := errors.Unwrap(err); inner != nil {
		return inner
	}

	return err
}

func run(ctx context.Context, pulumi pulumiReader, out io.Writer, args []string, terminal bool) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: recoverykit <stack>")
	}

	stack := args[0]

	// Validated before it is joined onto a path, rather than annotated after.
	// A stack name is a Pulumi identifier and nothing here needs it to be
	// more: one carrying a separator would look for a topology somewhere
	// other than infra/cluster and report the miss as a missing file, which
	// reads as "never created" for a name that was simply wrong.
	if !stackName.MatchString(stack) {
		return fmt.Errorf("%q is not a stack name: letters, digits, %s", stack, stackNameExtra)
	}

	if terminal {
		return secretout.Refuse("the cluster's recovery kit",
			"task cluster:recovery-kit stack="+stack,
			"hetzner/"+stack+"/recovery-kit")
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	kit := Kit{Stack: stack}

	// The bundle is the one part with no substitute, so its absence is an
	// error rather than a note: a kit without it opens nothing.
	bundle, err := pulumi.Bundle(ctx, stack)
	if err != nil {
		return fmt.Errorf("talos secrets bundle: %w", err)
	}

	kit.Bundle = bundle

	// The other two are noted when absent rather than fatal. A layer that was
	// never applied has no backup destination to record, and a kit that
	// refuses to print because of it would leave the operator with nothing on
	// the day they need the half that does exist.
	//
	// Absent, not unreadable: a backup tier that exists and could not be read
	// is an error, because the kit it would print lacks the restic password
	// while the tier holding it is right there.
	kit.Backup, kit.BackupMissing, err = backupOutputs(ctx, pulumi, stack)
	if err != nil {
		return fmt.Errorf("backup tier outputs: %w", err)
	}

	// #nosec G304,G703 -- the path is this file's own directory constant plus
	// a stack name checked against stackName above, so it cannot carry a
	// separator or a parent reference. Both codes are needed: G703's taint
	// analysis reports this separately from G304.
	topology, err := os.ReadFile(filepath.Join(TopologyDir, clusterspec.TopologyFile(stack)))
	if err != nil {
		kit.TopologyMissing = err.Error()
	} else {
		kit.Topology = topology
	}

	_, err = out.Write(kit.Document())

	return err
}

// backupOutputs reads the backup tier's outputs, and tells a tier that was
// never applied from one that could not be read.
//
// Only the first is a note. It is decided from Pulumi's own listing rather
// than from the wording of the error: the stack is absent when the project
// does not list it, and it has no outputs when it lists nothing under it.
// Anything else — a timeout, an expired login, a backend refusing — is
// returned, so the task fails instead of storing a kit that decrypts nothing.
func backupOutputs(ctx context.Context, pulumi pulumiReader, stack string) (outputs []byte, missing string, err error) {
	raw, readErr := pulumi.Outputs(ctx, BackupDir, stack)
	if readErr == nil {
		var decoded map[string]json.RawMessage
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil, "", fmt.Errorf("the outputs of %s in %s are not usable json: %w", stack, BackupDir, err)
		}

		if len(decoded) == 0 {
			return nil, "stack " + stack + " in " + BackupDir + " has no outputs: it was never applied, " +
				"or it was destroyed", nil
		}

		return raw, "", nil
	}

	stacks, listErr := pulumi.Stacks(ctx, BackupDir)
	if listErr != nil {
		return nil, "", errors.Join(readErr, listErr)
	}

	for _, listed := range stacks {
		if stackstatus.SameStack(listed, stack) {
			return nil, "", readErr
		}
	}

	return nil, "no stack " + stack + " in " + BackupDir + ": the layer was never applied", nil
}

// Kit is what a recovery needs from Pulumi's state, and what could not be
// read.
type Kit struct {
	Stack string

	// Bundle is the Talos secrets bundle. Required.
	Bundle []byte

	// Backup is every output of the backup tier, as JSON. BackupMissing
	// carries the reason when there is none.
	Backup        []byte
	BackupMissing string

	// Topology is the cluster description. TopologyMissing carries the reason
	// when the file is not there.
	Topology        []byte
	TopologyMissing string
}

// section separates the parts so a person can find one on the worst day of
// the year. A marker rather than YAML or JSON around the whole thing: two of
// the three parts are themselves documents in those formats, and nesting them
// would mean quoting a certificate authority.
const section = "───────────── "

// Document is the kit as one text, composed apart from the reads so it can be
// tested without a backend.
func (k Kit) Document() []byte {
	var out bytes.Buffer

	fmt.Fprintf(&out, "%sRECOVERY KIT · %s\n\n", section, k.Stack)
	out.WriteString(
		"What each part opens, and the order to use them in:\n\n" +
			"  1. talos-secrets — the cluster CA. `talosctl bootstrap --recover-from`\n" +
			"     accepts an etcd snapshot only against these, and the secrets inside a\n" +
			"     snapshot are ciphertext under the secretbox key here. Without it a\n" +
			"     snapshot restores nothing.\n" +
			"  2. backup-outputs — the Storage Box and the restic repository password.\n" +
			"     The snapshots are encrypted with it, and re-applying the layer\n" +
			"     generates a different one that does not open the old repository.\n" +
			"  3. topology — the cluster's shape. Gitignored, because it names the\n" +
			"     networks it is administered from, so a clone does not carry it.\n\n" +
			"This is not a state backup. `task cluster:state:export` covers the resource\n" +
			"graph, and answers a deleted stack; this answers a backend out of reach,\n" +
			"which an export encrypted by that backend's key does not.\n\n")

	fmt.Fprintf(&out, "%stalos-secrets\n", section)
	out.Write(ensureNewline(k.Bundle))

	fmt.Fprintf(&out, "\n%sbackup-outputs\n", section)

	if k.Backup != nil {
		out.Write(ensureNewline(k.Backup))
	} else {
		fmt.Fprintf(&out, "MISSING — the snapshots on the Storage Box cannot be decrypted "+
			"without this.\nApply infra/backup, then take this kit again.\n%s\n",
			indent(k.BackupMissing))
	}

	fmt.Fprintf(&out, "\n%stopology · %s\n", section, clusterspec.TopologyFile(k.Stack))

	if k.Topology != nil {
		out.Write(ensureNewline(k.Topology))
	} else {
		fmt.Fprintf(&out, "MISSING — no task in this repository runs without it.\n%s\n",
			indent(k.TopologyMissing))
	}

	return out.Bytes()
}

// ensureNewline keeps the next section marker on its own line whatever the
// part ended with.
func ensureNewline(part []byte) []byte {
	if len(part) > 0 && part[len(part)-1] == '\n' {
		return part
	}

	return append(part, '\n')
}

// indent sets a reason apart from the instruction above it.
func indent(reason string) string {
	if reason == "" {
		return ""
	}

	return "  " + strings.ReplaceAll(strings.TrimSpace(reason), "\n", "\n  ")
}
