package ci

// The cluster's own procedures, where two tasks have to agree about something
// no error would report: the name of a snapshot's sidecar, and which node a
// talosctl call is aimed at.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSnapshotSidecar_IsSpelledOnce holds the writer and the reader of a
// snapshot's .info file to one spelling.
//
// cluster:etcd:snapshot writes it and cluster:etcd:restore reads it, and a
// mismatch between them is not an error anybody sees: the restore simply
// stops printing what the snapshot was recorded as containing, which is the
// one thing that would say the file changed after it was written.
func TestSnapshotSidecar_IsSpelledOnce(t *testing.T) {
	t.Parallel()

	const (
		sidecarVar    = "_CL_SNAPSHOT_INFO"
		sidecarSuffix = ".info"
	)

	raw, err := os.ReadFile(filepath.Join("..", "..", "tasks", "cluster.task.yaml"))
	require.NoError(t, err)

	tasks := tasksIn(string(raw))

	for _, name := range []string{"etcd:snapshot", "etcd:restore"} {
		body, found := tasks[name]
		require.True(t, found, "no %s task to check", name)

		assert.Contains(t, body, sidecarVar,
			"%s does not use {{.%s}}, so it carries its own spelling of the sidecar's name",
			name, sidecarVar)

		// The var alone is not enough: a body can mention it in a message and
		// still build the path from a literal, which is exactly the drift
		// this is here to stop. The suffix itself lives in the vars block,
		// which is not part of any task body.
		assert.NotContains(t, body, sidecarSuffix,
			"%s spells the sidecar suffix %q itself instead of using {{.%s}}",
			name, sidecarSuffix, sidecarVar)
	}
}

// The guard that used to sit here — every hcloud verb that takes a node away
// must carry a prompt — went with the tasks. They are the shared library's
// `hcloud` module now, and a gate belongs where the thing it guards lives:
// this repository declares no hcloud task to check.

// talosctlCall matches one talosctl command, up to where the shell ends it: a
// pipe, a list operator or the close of a command substitution. Once
// continuation lines are joined, the line goes on into whatever the output is
// piped to, and `| tail -n 20` would otherwise read as a node flag.
var talosctlCall = regexp.MustCompile(`talosctl[^\n|;&)]*`)

// nodeTargeted matches the flag that names a node.
var nodeTargeted = regexp.MustCompile(`\s(?:-n|--nodes)\s`)

// lineContinuation is a shell line broken with a trailing backslash, and the
// indentation of the line that carries on.
var lineContinuation = regexp.MustCompile(`\\\n[ \t]*`)

// shellCommands joins every backslash-continued line into the one command a
// shell runs. Read line by line, `talosctl \` with --nodes on the next line
// was invisible, and --nodes and --endpoints on two continuation lines failed
// a call that had both.
func shellCommands(text string) []string {
	return strings.Split(lineContinuation.ReplaceAllString(text, " "), "\n")
}

// TestTalosctlCalls_NameAnEndpointWithEveryNode is a regression guard for a
// restore that could not restore.
//
// `talosctl --nodes X` alone connects to the endpoint in talosconfig and asks
// IT to proxy to X. Nodes reach each other over the private network, so a
// peer's public address is not a path the endpoint can route to — measured as
// `dial tcp <peer>:50000: i/o timeout` on the first reset of a three-member
// restore, before anything was wiped.
//
// Naming the node as its own endpoint is what works, and it is also the only
// thing that survives the procedure: etcd:restore wipes every control-plane
// node, so the endpoint is about to reboot and proxying through it stops
// working halfway.
//
// Invisible with one control-plane node, because that node IS the endpoint.
// That is why a single-node rehearsal passed and this had to be found on
// three.
func TestTalosctlCalls_NameAnEndpointWithEveryNode(t *testing.T) {
	t.Parallel()

	var checked int

	for _, path := range taskfiles(t) {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)

		for _, line := range shellCommands(string(raw)) {
			for _, call := range talosctlCall.FindAllString(line, -1) {
				if !nodeTargeted.MatchString(call) {
					continue
				}

				checked++

				assert.Contains(t, call, "--endpoints",
					"%s names a node without an endpoint:\n\t%s\nTalos will proxy through the "+
						"configured endpoint, which cannot route to a peer's public address — and "+
						"during a restore that endpoint is itself being wiped",
					filepath.Base(path), strings.TrimSpace(call))
			}
		}
	}

	assert.Positive(t, checked, "no talosctl call targets a node; this test is checking nothing")
}

// TestStateExport_CoversEveryStackAndTellsAbsenceFromFailure holds the three
// ways cluster:state:export reported a complete copy that was not one.
//
//   - It walked infra/cluster and the layers by hand, and infra/backup — the
//     one tier whose state holds a generated secret nothing else has, the
//     restic key — was never in the list.
//   - Every failed export was sent to /dev/null and reported as "has no
//     stack", so a backend that did not answer looked like a layer nobody
//     applied.
//   - Asking pulumi-kit whether the stack exists through `go run` cannot tell
//     the two apart either: go run exits 1 whatever the program exited with.
func TestStateExport_CoversEveryStackAndTellsAbsenceFromFailure(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "tasks", "cluster.task.yaml"))
	require.NoError(t, err)

	body, found := tasksIn(string(raw))["state:export"]
	require.True(t, found, "no state:export task to check")

	for _, list := range []string{".TIERS", ".LAYERS"} {
		assert.Contains(t, body, `splitList " " `+list,
			"state:export does not walk %s, so a project added there is never exported", list)
	}

	assert.NotContains(t, body, "infra/cluster ",
		"state:export names a tier by hand beside TIERS — the list it used to keep, and missed a tier in")
	assert.NotContains(t, body, "2>/dev/null",
		"state:export discards an error, which is how a failed export read as an absent stack")
	assert.NotContains(t, body, "{{.KIT_STACK}} exists",
		"state:export asks `exists` through go run, which exits 1 for both absent and failed")
	assert.Contains(t, body, "exists",
		"state:export no longer asks whether a stack exists before exporting it")
}

// TestClusterApply_DoesNotSayAFailedApplyChangedNothing keeps the failure
// message to what is true of every failure.
//
// Every non-zero `pulumi up` printed "a protected resource is the control
// plane or the API endpoint … Nothing was changed" and pointed at
// replace_control_plane=yes, which is --ignore-protect. A quota error or a
// timeout part-way through an apply has changed things, and the one advice
// given was to rerun with the control plane's only guard switched off.
func TestClusterApply_DoesNotSayAFailedApplyChangedNothing(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "tasks", "cluster.task.yaml"))
	require.NoError(t, err)

	body, found := tasksIn(string(raw))["apply"]
	require.True(t, found, "no apply task to check")

	assert.NotContains(t, strings.ToLower(body), "nothing was changed",
		"cluster:apply says nothing changed after a failed pulumi up, which is false for any "+
			"failure after the first resource")

	hint := strings.Index(body, "replace_control_plane=yes\"")
	condition := strings.Index(body, "If it refused")

	require.Positive(t, hint, "cluster:apply no longer names replace_control_plane=yes on failure")
	assert.True(t, condition >= 0 && condition < hint,
		"cluster:apply offers replace_control_plane=yes without saying it is for a refused protected "+
			"replacement only")
}

// TestNodeTasks_WalkEveryNode keeps the tasks that promise every node from
// reaching one.
//
// The generated talosconfig names the first control-plane node as its
// endpoint and its only node. stop and reboot prompted "every node of <stack>"
// and ran a bare `talosctl shutdown` / `talosctl reboot`, and encryption:check
// judged the whole cluster from one disk — the one kind of volume state that
// differs node by node.
func TestNodeTasks_WalkEveryNode(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "tasks", "cluster.task.yaml"))
	require.NoError(t, err)

	tasks := tasksIn(string(raw))

	for _, name := range []string{"stop", "reboot", "encryption:check"} {
		body, found := tasks[name]
		require.True(t, found, "no %s task to check", name)

		assert.Contains(t, body, "{{._CL_NODES}}", "%s does not walk the node list", name)

		var calls int

		for _, line := range strings.Split(body, "\n") {
			if !strings.Contains(line, "talosctl ") || isComment(line) {
				continue
			}

			calls++

			assert.Contains(t, line, "--nodes",
				"%s calls talosctl without naming a node, so it reaches the talosconfig's one:\n\t%s",
				name, strings.TrimSpace(line))
		}

		assert.Positive(t, calls, "%s calls talosctl nowhere; this test proved nothing about it", name)
	}
}

// TestCredentialFiles_AreWrittenWholeAndKeptOffTheCommandLine holds the three
// tasks that write a cluster credential.
//
//   - kubeconfig and talosconfig redirected pulumi straight into the target,
//     which existed at the default umask until a chmod and was left truncated
//     when the output could not be read. They go through a mktemp file now.
//   - kubeconfig:add passed the cluster-admin key to `kubectl config set` as
//     an argument, where `ps` shows it to every user on the machine.
func TestCredentialFiles_AreWrittenWholeAndKeptOffTheCommandLine(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "tasks", "cluster.task.yaml"))
	require.NoError(t, err)

	tasks := tasksIn(string(raw))

	for _, name := range []string{"kubeconfig", "talosconfig"} {
		body, found := tasks[name]
		require.True(t, found, "no %s task to check", name)

		assert.Contains(t, body, "mktemp", "%s does not write through a temporary file", name)
		assert.NotContains(t, body, `--show-secrets > "{{.ROOT}}/`+name+`"`,
			"%s redirects the credential straight into its target", name)
	}

	body, found := tasks["kubeconfig:add"]
	require.True(t, found, "no kubeconfig:add task to check")

	assert.NotContains(t, body, "client-key-data\" \"$",
		"kubeconfig:add passes the client key to kubectl as an argument")
	assert.Contains(t, body, "--client-key=", "kubeconfig:add no longer hands kubectl the key as a file")
}

// TestTaskfiles_DoNotCallUmask keeps out a builtin Task's shell does not have.
//
// Task runs every command through its own shell interpreter, not /bin/sh, and
// that interpreter has no `umask`: the task stops with "unsupported builtin"
// before doing anything. Found by running a task that used it.
func TestTaskfiles_DoNotCallUmask(t *testing.T) {
	t.Parallel()

	var scanned int

	for _, path := range taskfiles(t) {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)

		scanned++

		for i, line := range strings.Split(string(raw), "\n") {
			if isComment(line) {
				continue
			}

			assert.NotRegexp(t, `(^|[;&|\s])umask\b`, line,
				"%s:%d calls umask, which Task's shell interpreter does not have", relativeToRoot(path), i+1)
		}
	}

	require.Positive(t, scanned, "no taskfile was read; this test is checking nothing")
}

// TestEtcdRestore_WipesTheControlPlaneTogether holds the restore to resetting
// every node in one go.
//
// Reset one after another, each wiped node came back to the members not yet
// wiped and rejoined them with their data: no node reached Preparing, etcd
// kept the state the restore was meant to discard, and the loop wiped the
// whole control plane pass after pass until it was stopped.
func TestEtcdRestore_WipesTheControlPlaneTogether(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "tasks", "cluster.task.yaml"))
	require.NoError(t, err)

	body, found := tasksIn(string(raw))["etcd:restore"]
	require.True(t, found, "no etcd:restore task to check")

	var reset string

	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, " reset ") && !isComment(line) {
			reset = strings.TrimSpace(line)
		}
	}

	require.NotEmpty(t, reset, "etcd:restore resets no node")
	assert.True(t, strings.HasSuffix(reset, "&"), "the reset runs in the foreground, one node at a time:\n\t%s", reset)
	assert.Contains(t, reset, "--wait", "without --wait the next pass reads a node still shutting down")

	_, after, _ := strings.Cut(body, reset)
	assert.Regexp(t, `(?m)^\s*wait\b`, after, "the parallel resets are not waited for")
	assert.Contains(t, body, `if [ -z "$unreachable" ]`, "a node is wiped while another is still rebooting")
}

// TestEtcdDownload_IsUploadsOtherHalf holds the task that reads a snapshot
// back from the Storage Box to what upload wrote.
//
// Upload existed alone: the copy on the box could be written and never read
// back by anything in this repository, which is the copy a recovery needs
// when the machine that took the snapshot is gone. Both tasks reach the
// repository through one snippet, so they cannot disagree on where it is.
func TestEtcdDownload_IsUploadsOtherHalf(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "tasks", "cluster.task.yaml"))
	require.NoError(t, err)

	tasks := tasksIn(string(raw))

	for _, name := range []string{"etcd:upload", "etcd:download"} {
		body, found := tasks[name]
		require.True(t, found, "no %s task", name)
		assert.Contains(t, body, "{{._CL_BACKUP_REPO}}", "%s reaches the repository its own way", name)
	}

	download := tasks["etcd:download"]
	assert.Regexp(t, `restic restore "\$snapshot_id" --host "\$cluster" --tag etcd`, download,
		"download reads another cluster's snapshots, or ones upload never tagged")
	assert.Contains(t, download, `tools/etcd" verify --cluster "$cluster"`,
		"a downloaded file is handed to etcd:restore without being read back")
	assert.Contains(t, download, `cmp -s "$found" "$target"`,
		"download overwrites a different local snapshot of the same name")
}
