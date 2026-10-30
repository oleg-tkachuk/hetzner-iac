// Command smoke asks whether a cluster this repository built can actually run
// a workload, and exits non-zero when it cannot.
//
// A thin shell around internal/pkg/clustersmoke: the judgements and the client-go calls
// live there, where they are tested. What is here is argument handling and the
// report's shape on a terminal.
//
// It exists because `pulumi up` going green is not the same claim. It went
// green on a three-node cluster whose hcloud CSI controller was in
// CrashLoopBackOff — every resource created, every pod Running, and no volume
// obtainable. Nothing said so, because nothing asked for one.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/term"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clustersmoke"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"
)

// defaultKubeconfig is what `task cluster:kubeconfig` writes and what the e2e
// suite reads. Named here rather than assumed so the flag's help can say it.
const defaultKubeconfig = "./kubeconfig"

func main() {
	kubeconfig := flag.String("kubeconfig", defaultKubeconfig,
		"path to the kubeconfig; empty uses the standard rules, which respect KUBECONFIG")
	kubeContext := flag.String("context", "", "kubeconfig context; empty uses its current-context")
	storageClass := flag.String("storage-class", platform.StorageClass,
		"the class the probe claim asks for")
	timeout := flag.Duration("bind-timeout", clustersmoke.DefaultBindTimeout,
		"how long to wait for the probe claim to reach Bound")
	verbose := flag.Bool("v", false, "print each probe as it is applied, and the API server's warnings")

	flag.Parse()

	passed, err := run(invocation{
		kubeconfig: *kubeconfig, kubeContext: *kubeContext, storageClass: *storageClass,
		timeout: *timeout, verbose: *verbose,
	}, liveDeps())

	// Exiting here rather than inside run, so run's deferred cancel actually
	// runs. os.Exit does not unwind defers, and the first version called it
	// from inside run with a `defer cancel()` above it — the context was never
	// cancelled on the failing path.
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// A failed check exits without an "error:" line: the report is already
	// printed and says which check failed, and a second line would add
	// nothing.
	os.Exit(exitCode(passed))
}

// exitPassed is the status for a cluster that passed every check it reached.
const exitPassed = 0

// exitFailed is the status for "the checks ran and the cluster did not pass",
// kept distinct from the 1 that means "the checks could not run at all" — a
// missing kubeconfig, an unreachable API server. A caller that cannot tell
// those apart retries the wrong one.
//
// Worth knowing, because it is measurable and surprising: neither wrapper this
// repository uses passes it through. `go run` prints "exit status 2" to stderr
// and itself exits 1, and `task` reports any non-zero child as "exit status 1".
// Measured both. So `task cluster:smoke` distinguishes nothing, and a caller
// that needs the difference has to build the binary:
//
//	go build -o smoke ./tools/smoke && ./smoke
//
// The code is kept anyway — it is the correct thing for a CLI to return, and it
// costs nothing — rather than flattened to match the wrappers.
const exitFailed = 2

// exitCode is the status for checks that ran: exitFailed when any failed.
func exitCode(passed bool) int {
	if passed {
		return exitPassed
	}

	return exitFailed
}

// invocation is the command line, named.
type invocation struct {
	kubeconfig, kubeContext, storageClass string
	timeout                               time.Duration
	verbose                               bool
}

// deps are what run reaches outside the process, so a test can stand in for
// each one.
type deps struct {
	// smoke runs the checks against the cluster the options name.
	smoke func(ctx context.Context, opts clustersmoke.Options) (clustersmoke.Report, error)
	// out receives the report; progress receives -v's lines.
	out, progress io.Writer
	paint         clustersmoke.Paint
}

// liveDeps are the real cluster and terminal.
func liveDeps() deps {
	return deps{smoke: smokeCluster, out: os.Stdout, progress: os.Stderr, paint: painter(colour())}
}

// smokeCluster builds a Runner from a kubeconfig and runs every check.
func smokeCluster(ctx context.Context, opts clustersmoke.Options) (clustersmoke.Report, error) {
	runner, err := clustersmoke.New(opts)
	if err != nil {
		return nil, err
	}

	return runner.Run(ctx), nil
}

// colour is whether stdout is a terminal that has not asked for none —
// https://no-color.org.
func colour() bool {
	return os.Getenv("NO_COLOR") == "" && term.IsTerminal(int(os.Stdout.Fd()))
}

// painter is ANSI on a colour terminal and Plain everywhere else.
func painter(colour bool) clustersmoke.Paint {
	if colour {
		return clustersmoke.ANSI
	}

	return clustersmoke.Plain
}

// run executes the checks and prints the report. passed is false when any
// check failed; err means the checks could not run, or the report could not
// be written.
func run(inv invocation, d deps) (passed bool, err error) {
	// Progress only when asked for: the probes it names are the check's own
	// machinery, and the report below says what each one found.
	var logf func(string, ...any)
	if inv.verbose {
		logf = func(format string, args ...any) {
			// Progress only: a line that cannot be written must not stop the checks.
			_, _ = fmt.Fprintf(d.progress, "  · "+format+"\n", args...)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	report, err := d.smoke(ctx, clustersmoke.Options{
		Kubeconfig:   inv.kubeconfig,
		Context:      inv.kubeContext,
		StorageClass: inv.storageClass,
		BindTimeout:  inv.timeout,
		Logf:         logf,
	})
	if err != nil {
		return false, err
	}

	// Rendered with a count of each verdict, skipped apart from passed:
	// "2 passed, 1 skipped" is three checks and two answers, and rounding that
	// to "passed" is the failure this whole tool exists to avoid.
	if err := clustersmoke.Render(d.out, report, d.paint); err != nil {
		return false, err
	}

	return !report.Failed(), nil
}
