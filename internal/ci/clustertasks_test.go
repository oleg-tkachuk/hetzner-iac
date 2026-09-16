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

// nodeTargeted matches a talosctl invocation that names a node.
var nodeTargeted = regexp.MustCompile(`talosctl[^\n]*(?:-n |--nodes )`)

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

		for _, line := range strings.Split(string(raw), "\n") {
			if !nodeTargeted.MatchString(line) {
				continue
			}

			checked++

			assert.Contains(t, line, "--endpoints",
				"%s names a node without an endpoint:\n\t%s\nTalos will proxy through the "+
					"configured endpoint, which cannot route to a peer's public address — and "+
					"during a restore that endpoint is itself being wiped",
				filepath.Base(path), strings.TrimSpace(line))
		}
	}

	assert.Positive(t, checked, "no talosctl call targets a node; this test is checking nothing")
}
