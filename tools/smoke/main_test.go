package main

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clustersmoke"
)

// probeTimeout is a bind timeout no default equals, so a test can see it
// reached the runner.
const probeTimeout = 7 * time.Second

// probe is the line a fake runner logs when progress is on.
const probe = "applying probe claim"

var errUnreachable = errors.New("unreachable API server")

var (
	passedNodes  = clustersmoke.Result{Name: "every node is Ready", Status: clustersmoke.StatusPassed, Detail: "3 nodes Ready"}
	skippedCRDs  = clustersmoke.Result{Name: "operators are healthy", Status: clustersmoke.StatusSkipped, Detail: "no CRDs"}
	failedVolume = clustersmoke.Result{Name: "a volume can be bound", Status: clustersmoke.StatusFailed, Detail: "Pending after 2m"}
)

// fakeDeps answers every run with report, and records the options it
// was given.
func fakeDeps(report clustersmoke.Report, seen *clustersmoke.Options) (deps, *bytes.Buffer, *bytes.Buffer) {
	var out, progress bytes.Buffer

	return deps{
		smoke: func(ctx context.Context, opts clustersmoke.Options) (clustersmoke.Report, error) {
			*seen = opts
			if opts.Logf != nil {
				opts.Logf("%s", probe)
			}

			return report, ctx.Err()
		},
		out: &out, progress: &progress, paint: clustersmoke.Plain,
	}, &out, &progress
}

// rendered is the report as the tool prints it on a non-terminal.
func rendered(t *testing.T, report clustersmoke.Report) string {
	t.Helper()

	var want bytes.Buffer
	require.NoError(t, clustersmoke.Render(&want, report, clustersmoke.Plain))

	return want.String()
}

func TestRun_DecidesFromTheReport(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		report   clustersmoke.Report
		passed   bool
		exitCode int
	}{
		{"every check passed", clustersmoke.Report{passedNodes}, true, exitPassed},
		{"a skip is not a failure", clustersmoke.Report{passedNodes, skippedCRDs}, true, exitPassed},
		{"one failure fails the run", clustersmoke.Report{passedNodes, skippedCRDs, failedVolume}, false, exitFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var opts clustersmoke.Options

			d, out, progress := fakeDeps(tc.report, &opts)

			passed, err := run(invocation{}, d)

			require.NoError(t, err)
			assert.Equal(t, tc.passed, passed)
			assert.Equal(t, tc.exitCode, exitCode(passed))
			assert.Equal(t, rendered(t, tc.report), out.String())
			assert.Empty(t, progress.String(), "progress printed without -v")
		})
	}
}

func TestRun_PrintsEveryVerdictCount(t *testing.T) {
	t.Parallel()

	var opts clustersmoke.Options

	d, out, _ := fakeDeps(clustersmoke.Report{passedNodes, skippedCRDs, failedVolume}, &opts)

	_, err := run(invocation{}, d)

	require.NoError(t, err)
	assert.Contains(t, out.String(), "✖ a volume can be bound\n      Pending after 2m\n")
	assert.Contains(t, out.String(), "1 passed · 1 skipped · 1 failed")
}

func TestRun_PassesTheCommandLineToTheRunner(t *testing.T) {
	t.Parallel()

	var opts clustersmoke.Options

	d, _, progress := fakeDeps(clustersmoke.Report{passedNodes}, &opts)

	_, err := run(invocation{
		kubeconfig: "./kubeconfig", kubeContext: "admin@platform-dev", storageClass: "hcloud-volumes",
		timeout: probeTimeout, verbose: true,
	}, d)

	require.NoError(t, err)
	assert.Equal(t, "./kubeconfig", opts.Kubeconfig)
	assert.Equal(t, "admin@platform-dev", opts.Context)
	assert.Equal(t, "hcloud-volumes", opts.StorageClass)
	assert.Equal(t, probeTimeout, opts.BindTimeout)
	assert.Equal(t, "  · "+probe+"\n", progress.String())
}

func TestRun_ChecksThatCannotRunAreAnErrorWithNoReport(t *testing.T) {
	t.Parallel()

	var opts clustersmoke.Options

	d, out, _ := fakeDeps(nil, &opts)
	d.smoke = func(context.Context, clustersmoke.Options) (clustersmoke.Report, error) {
		return nil, errUnreachable
	}

	passed, err := run(invocation{}, d)

	require.ErrorIs(t, err, errUnreachable)
	assert.False(t, passed)
	assert.Empty(t, out.String())
}

// failingWriter refuses every write.
type failingWriter struct{}

var errWrite = errors.New("stdout closed")

func (failingWriter) Write([]byte) (int, error) { return 0, errWrite }

func TestRun_AReportThatCannotBeWrittenIsAnError(t *testing.T) {
	t.Parallel()

	var opts clustersmoke.Options

	d, _, _ := fakeDeps(clustersmoke.Report{passedNodes}, &opts)
	d.out = failingWriter{}

	passed, err := run(invocation{}, d)

	require.ErrorIs(t, err, errWrite)
	assert.False(t, passed)
}

func TestPainter(t *testing.T) {
	t.Parallel()

	const text = "1 passed"

	assert.Equal(t, text, painter(false)(clustersmoke.StatusPassed, text))
	assert.Equal(t, clustersmoke.ANSI(clustersmoke.StatusPassed, text), painter(true)(clustersmoke.StatusPassed, text))
}
