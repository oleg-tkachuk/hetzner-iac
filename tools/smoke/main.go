// Command smoke asks whether a cluster this repository built can actually run
// a workload, and exits non-zero when it cannot.
//
// A thin shell around pkg/clustersmoke: the judgements and the client-go calls
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
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/clustersmoke"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/platform"
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

	flag.Parse()

	err := run(*kubeconfig, *kubeContext, *storageClass, *timeout)

	// Exiting here rather than inside run, so run's deferred cancel actually
	// runs. os.Exit does not unwind defers, and the first version called it
	// from inside run with a `defer cancel()` above it — the context was never
	// cancelled on the failing path.
	switch {
	case errors.Is(err, errChecksFailed):
		// The report is already printed and says which check failed; a second
		// "error:" line above it would add nothing.
		os.Exit(exitFailed)
	case err != nil:
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// errChecksFailed means the checks ran and the cluster did not pass, as
// distinct from the checks not running at all.
var errChecksFailed = errors.New("cluster failed the smoke checks")

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

func run(kubeconfig, kubeContext, storageClass string, timeout time.Duration) error {
	runner, err := clustersmoke.New(clustersmoke.Options{
		Kubeconfig:   kubeconfig,
		Context:      kubeContext,
		StorageClass: storageClass,
		BindTimeout:  timeout,
		Logf:         func(format string, args ...any) { fmt.Printf("  "+format+"\n", args...) },
	})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	report := runner.Run(ctx)

	for _, result := range report {
		fmt.Printf("%s %s — %s\n", result.Status.Glyph(), result.Name, result.Detail)
	}

	// The summary counts skipped separately: "2 passed, 1 skipped" is three
	// checks and two answers, and rounding that to "passed" is the failure
	// this whole tool exists to avoid.
	fmt.Printf("\n%d checks, %d skipped\n", len(report), report.Skipped())

	if report.Failed() {
		return errChecksFailed
	}

	return nil
}
