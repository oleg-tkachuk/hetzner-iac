// Command recoverykit prints the parts of a cluster that live nowhere but
// Pulumi's state, in one document meant for a password store.
//
// It exists because the backups this repository already takes are not
// self-sufficient, and the dependency runs the wrong way:
//
//   - an etcd snapshot on the Storage Box is encrypted by restic
//   - restic's repository password is a random.NewRandomPassword generated
//     into layers/60-backup's state, and re-applying that layer generates a
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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/secretout"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/talossecrets"
)

// stackName is what may be joined onto a path. Pulumi's own stack names allow
// letters, digits, hyphens, underscores and periods; a period is permitted
// here but a leading one is not, which is what keeps `..` out.
var stackName = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*$`)

// stackNameExtra names the rest for the error message, so the message and the
// pattern cannot drift into disagreeing about what is allowed.
const stackNameExtra = "hyphens, underscores and periods, and not a leading period"

// timeout covers two Pulumi reads, each decrypting through the backend.
const timeout = 120 * time.Second

// BackupDir is the layer whose outputs hold the backup credentials. Every
// output of it is taken rather than a named few: they are all generated, none
// is ever typed, and a list of names here would be a third copy of them.
const BackupDir = "layers/60-backup"

// TopologyDir is where the committed-shaped-but-gitignored topology lives.
const TopologyDir = "infra/cluster"

func main() {
	if err := run(context.Background(), os.Args[1:], secretout.IsTerminal()); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, terminal bool) error {
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
	bundle, err := talossecrets.Bundle(ctx, stack)
	if err != nil {
		return fmt.Errorf("talos secrets bundle: %w", err)
	}

	kit.Bundle = bundle

	// The other two are noted when missing rather than fatal. A layer that was
	// never applied has no backup destination to record, and a kit that
	// refuses to print because of it would leave the operator with nothing on
	// the day they need the half that does exist.
	backup, err := stackOutputs(ctx, BackupDir, stack)
	if err != nil {
		kit.BackupMissing = err.Error()
	} else {
		kit.Backup = backup
	}

	// #nosec G304,G703 -- the path is this file's own directory constant plus
	// a stack name checked against stackName above, so it cannot carry a
	// separator or a parent reference. Both codes are needed: G703's taint
	// analysis reports this separately from G304.
	topology, err := os.ReadFile(filepath.Join(TopologyDir, "cluster."+stack+".yaml"))
	if err != nil {
		kit.TopologyMissing = err.Error()
	} else {
		kit.Topology = topology
	}

	_, err = os.Stdout.Write(kit.Document())

	return err
}

// stackOutputs reads every output of a stack, secrets decrypted.
//
// The CLI as an argument vector and its JSON, rather than the automation API:
// this is the same call tasks/cluster.task.yaml already makes for the upload
// task, and `--show-secrets` on `stack output` is the documented way to get
// the generated passwords out. Nothing is interpolated into a shell.
func stackOutputs(ctx context.Context, dir, stack string) ([]byte, error) {
	// #nosec G204,G702 -- the arguments are literals from this file plus a
	// stack name, passed as a VECTOR: there is no shell to interpret any of
	// it, so a name carrying shell metacharacters reaches pulumi as one
	// argument and is rejected there. Both codes are needed — G702's taint
	// analysis reports this separately from G204, which is how the pinned
	// linter caught a G204-only directive here.
	cmd := exec.CommandContext(ctx, "pulumi", "--non-interactive",
		"--cwd", dir, "--stack", stack, "stack", "output", "--json", "--show-secrets")

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			return nil, fmt.Errorf("pulumi stack output in %s: %w", dir, err)
		}

		return nil, fmt.Errorf("pulumi stack output in %s: %w\n%s", dir, err, message)
	}

	return out, nil
}

// Kit is what a recovery needs from Pulumi's state, and what could not be
// read.
type Kit struct {
	Stack string

	// Bundle is the Talos secrets bundle. Required.
	Bundle []byte

	// Backup is every output of the backup layer, as JSON. BackupMissing
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
			"without this.\nApply layers/60-backup, then take this kit again.\n%s\n",
			indent(k.BackupMissing))
	}

	fmt.Fprintf(&out, "\n%stopology · cluster.%s.yaml\n", section, k.Stack)

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
