package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
)

var errBoom = errors.New("boom")

const (
	pinned       = "v1.14.0"
	fakeTalosctl = "/opt/talosctl"
	topologyFile = "cluster.dev.yaml"
)

const topologyYAML = `
apiVersion: hetzner-iac/v1
kind: Cluster
metadata:
  name: platform-test
placement:
  location: hel1
network:
  adminCIDRs: [203.0.113.4/32]
talos:
  version: ` + pinned + `
controlPlane:
  count: 1
  serverType: cx23
`

// talosctlFake answers like talosctl, failing the step named in failing.
type talosctlFake struct {
	// version is what `version --client --short` prints; empty fails it.
	version string
	// longVersion is what the long form prints.
	longVersion string
	// failing is the first argument of the combined call that fails.
	failing string
	// exited says the failing call ran and exited non-zero.
	exited bool
	// passes is how many calls of the failing kind succeed first.
	passes int
	calls  [][]string
	// path is the binary run asked for.
	path   string
	stdout bytes.Buffer
	stderr bytes.Buffer
}

func (f *talosctlFake) runner() runner {
	return runner{
		output: func(_ context.Context, args ...string) ([]byte, error) {
			if len(args) == 3 && f.version != "" {
				return []byte(f.version), nil
			}

			if len(args) == 2 && f.longVersion != "" {
				return []byte(f.longVersion), nil
			}

			return nil, errBoom
		},
		combined: func(_ context.Context, args ...string) ([]byte, bool, error) {
			f.calls = append(f.calls, args)
			if args[0] == f.failing && f.passes == 0 {
				return []byte("unknown keys found\n"), f.exited, errBoom
			}

			if args[0] == f.failing {
				f.passes--
			}

			return nil, false, nil
		},
		stdout: &f.stdout,
		stderr: &f.stderr,
	}
}

func (f *talosctlFake) deps() deps {
	return deps{
		talosctlPath: func() (string, error) { return fakeTalosctl, nil },
		topology:     clusterspec.LoadTopology,
		runner: func(talosctl string) runner {
			f.path = talosctl

			return f.runner()
		},
	}
}

// topologyDir is a directory holding one valid topology.
func topologyDir(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, topologyFile), []byte(topologyYAML), 0o600))

	return dir
}

func TestRun_ValidatesTheControlPlaneAndAWorker(t *testing.T) {
	t.Parallel()

	fake := &talosctlFake{version: "Talos v1.14.2"}

	require.NoError(t, run(topologyDir(t), fake.deps()))

	assert.Equal(t, fakeTalosctl, fake.path, "the talosctl it resolved")

	assert.Equal(t, "ok    cluster.dev.yaml                   controlplane\n"+
		"ok    cluster.dev.yaml                   worker pool probe\n", fake.stdout.String())
	require.Len(t, fake.calls, 5, "gen config, then a patch and a validate per machine type")
	assert.Equal(t, []string{"validate", "--mode", "cloud"},
		[]string{fake.calls[2][0], fake.calls[2][3], fake.calls[2][4]})
}

func TestRun_StopsAtWhatItCannotReach(t *testing.T) {
	t.Parallel()

	missing := (&talosctlFake{}).deps()
	missing.talosctlPath = func() (string, error) { return "", errBoom }
	require.ErrorIs(t, run(topologyDir(t), missing), errBoom)

	require.Error(t, run(filepath.Join(t.TempDir(), "absent"), (&talosctlFake{}).deps()))

	unreadable := (&talosctlFake{}).deps()
	unreadable.topology = func(string) (*clusterspec.Topology, error) { return nil, errBoom }
	err := run(topologyDir(t), unreadable)
	require.ErrorIs(t, err, errBoom)
	assert.Contains(t, err.Error(), topologyFile)
}

func TestRun_NamesTheMachineTalosRejected(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		failing string
		exited  bool
		passes  int
		want    string
	}{
		{"gen", true, 0, "talosctl gen config: unknown keys found"},
		{"machineconfig", true, 0, "cluster.dev.yaml (controlplane): applying the patches failed: unknown keys found"},
		{"validate", true, 0, "talos rejected the configuration:\n      unknown keys found"},
		{"validate", false, 0, "cluster.dev.yaml (controlplane): boom"},
		{"validate", true, 1, "cluster.dev.yaml (worker pool probe): talos rejected"},
	} {
		fake := &talosctlFake{version: "Talos " + pinned, failing: tc.failing, exited: tc.exited, passes: tc.passes}

		err := run(topologyDir(t), fake.deps())
		require.Error(t, err, tc.want)
		assert.Contains(t, err.Error(), tc.want)
		assert.NotContains(t, fake.stdout.String(), "worker", "a rejected worker is not reported as ok")
	}
}

func TestCheckVersion(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	matching := &talosctlFake{longVersion: "Client:\n\tTag:         v1.14.3\n"}
	require.NoError(t, checkVersion(ctx, matching.runner(), pinned), "the long form, when --short is refused")

	err := checkVersion(ctx, (&talosctlFake{}).runner(), pinned)
	require.ErrorIs(t, err, errBoom)
	assert.Contains(t, err.Error(), "talosctl version")

	err = checkVersion(ctx, (&talosctlFake{version: "Talos dev\n"}).runner(), pinned)
	require.Error(t, err)
	assert.Equal(t, `could not read the talosctl version from "Talos dev"`, err.Error())

	older := &talosctlFake{version: "Talos v1.13.10"}
	err = checkVersion(ctx, older.runner(), pinned)
	require.Error(t, err)
	assert.Equal(t, "talosctl is v1.13.10 but the topology pins Talos "+pinned, err.Error())
	assert.True(t, strings.HasPrefix(older.stderr.String(), "Talos moves configuration between documents"))
	assert.Contains(t, older.stderr.String(), "  task cluster:talosctl:install\n")
}

func TestTargetDir_DefaultsToTheClusterTier(t *testing.T) {
	t.Parallel()

	assert.Equal(t, clusterspec.ClusterDir, targetDir(nil))
	assert.Equal(t, "elsewhere", targetDir([]string{"elsewhere"}))
}
