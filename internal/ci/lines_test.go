// The tests for .github/scripts/lines.awk.
//
// The shared awk program is the one piece of this repository written in a
// language with no compiler and no type checker, so it is the piece most in
// need of a test. An awk program nobody tests is a shell pipeline with extra
// steps — and pipelines returning the wrong answer quietly is the problem the
// file was written to solve.
package ci

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// linesRun feeds stdin to lines.awk with the given op and returns its output and
// exit code.
func linesRun(t *testing.T, op, stdin string, files ...string) (string, int) {
	t.Helper()

	return linesRunWith(t, op, "", stdin, files...)
}

// linesRunAllowing is linesRun with the staged-binaries allowlist set.
func linesRunAllowing(t *testing.T, op, stdin, allow string) (string, int) {
	t.Helper()

	return linesRunWith(t, op, allow, stdin)
}

func linesRunWith(t *testing.T, op, allow, stdin string, files ...string) (string, int) {
	t.Helper()

	script := filepath.Join("..", "..", ".github", "scripts", "lines.awk")

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	args := []string{"-f", script, "-v", "op=" + op}
	if allow != "" {
		args = append(args, "-v", "allow="+allow)
	}

	args = append(args, files...)
	cmd := exec.CommandContext(ctx, "awk", args...)
	cmd.Stdin = strings.NewReader(stdin)

	out, err := cmd.CombinedOutput()

	code := 0

	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else {
		require.NoError(t, err)
	}

	return string(out), code
}

func TestLinesAvailMB_ReadsTheFirstDataRow(t *testing.T) {
	t.Parallel()

	// The shape df prints: a header, then the number with its unit suffix.
	out, code := linesRun(t, "avail-mb", "Avail\n36109M\n")

	assert.Equal(t, 0, code)
	assert.Equal(t, "36109", strings.TrimSpace(out))
}

func TestLinesAvailMB_IgnoresASecondFilesystem(t *testing.T) {
	t.Parallel()

	// A df given more than one path prints more than one row. Summing them
	// would report space that is not on the filesystem the caller asked about.
	out, code := linesRun(t, "avail-mb", "Avail\n36109M\n99999M\n")

	assert.Equal(t, 0, code)
	assert.Equal(t, "36109", strings.TrimSpace(out))
}

func TestLinesAvailMB_FailsWhenDfPrintsNothing(t *testing.T) {
	t.Parallel()

	// The improvement over `tail -1 | tr -dc '0-9'`, which returned an empty
	// string that arithmetic then treated as zero — reporting a full disk as
	// an empty one.
	out, code := linesRun(t, "avail-mb", "Avail\n")

	assert.Equal(t, 1, code)
	assert.Contains(t, out, "no data row")
}

func TestLinesCacheWriters_FindsExactlyOneInTheRealWorkflows(t *testing.T) {
	t.Parallel()

	// The guard this backs: an Actions cache key is immutable, so whichever
	// job saves first owns it, and the job that compiles the tree is by
	// definition slower than one that does not.
	workflows, err := filepath.Glob(filepath.Join("..", "..", ".github", "workflows", "*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, workflows)

	out, code := linesRun(t, "cache-writers", "", workflows...)

	assert.Equal(t, 0, code)
	assert.Contains(t, out, "count=1")
}

func TestLinesCacheWriters_DoesNotMatchItsOwnPattern(t *testing.T) {
	t.Parallel()

	// The shell version counted the string inside its own source on the first
	// attempt. The pattern is anchored to the start of a line for that reason,
	// so a mention in prose does not count.
	out, code := linesRun(t, "cache-writers", "a comment about cache-mode: save\n")

	assert.Equal(t, 0, code)
	assert.Contains(t, out, "count=0")
}

func TestLinesOp_IsRequiredAndChecked(t *testing.T) {
	t.Parallel()

	// Silently printing nothing for a mistyped op would reintroduce the exact
	// failure this file replaced.
	out, code := linesRun(t, "", "x\n")
	assert.Equal(t, 2, code)
	assert.Contains(t, out, "op=<name> is required")

	out, code = linesRun(t, "avail-megabytes", "x\n")
	assert.Equal(t, 2, code)
	assert.Contains(t, out, "unknown op")
}

func TestLinesReverseWords_LastFirst(t *testing.T) {
	t.Parallel()

	// The layer list arrives as one space-separated string from the Taskfile,
	// so tokens rather than lines: piping it through `tr` first would be the
	// pipeline this op exists to remove.
	out, code := linesRun(t, "reverse-words", "10-node-platform 30-cluster-services 40-ingress")

	assert.Equal(t, 0, code)
	assert.Equal(t, "40-ingress\n30-cluster-services\n10-node-platform", strings.TrimSpace(out))
}

func TestLinesReverseWords_TakesSeveralLinesToo(t *testing.T) {
	t.Parallel()

	// Same answer whether the list arrives on one line or many, which is what
	// makes it a drop-in for `tac` and for `tail -r`.
	out, code := linesRun(t, "reverse-words", "a b\nc\n")

	assert.Equal(t, 0, code)
	assert.Equal(t, "c\nb\na", strings.TrimSpace(out))
}

func TestLinesReverseWords_EmptyInputIsEmptyOutput(t *testing.T) {
	t.Parallel()

	// Not an error: a layer list can legitimately be empty, and the loop that
	// consumes this then runs zero times.
	out, code := linesRun(t, "reverse-words", "")

	assert.Equal(t, 0, code)
	assert.Empty(t, strings.TrimSpace(out))
}

func TestLinesStagedBinaries_FailsAndNamesThem(t *testing.T) {
	t.Parallel()

	// The shape `git diff --cached --numstat` prints: counts for text, a pair
	// of dashes for anything git calls binary.
	out, code := linesRun(t, "staged-binaries", "2\t0\tmain.go\n-\t-\tstray-bin\n-\t-\tlogo.png\n")

	assert.Equal(t, 1, code, "a staged binary must fail the commit")
	assert.Contains(t, out, "stray-bin")
	assert.Contains(t, out, "logo.png")
	// The remedy, because the cause is almost always the same one.
	assert.Contains(t, out, "go build")
}

func TestLinesStagedBinaries_TextOnlyPasses(t *testing.T) {
	t.Parallel()

	out, code := linesRun(t, "staged-binaries", "2\t0\tmain.go\n14\t3\tREADME.md\n")

	assert.Equal(t, 0, code)
	assert.Empty(t, strings.TrimSpace(out))
}

func TestLinesStagedBinaries_NothingStagedPasses(t *testing.T) {
	t.Parallel()

	// An empty commit, or a commit of deletions only. Not a failure.
	_, code := linesRun(t, "staged-binaries", "")

	assert.Equal(t, 0, code)
}

func TestLinesStagedBinaries_CatchesWhatTheOldPatternMissed(t *testing.T) {
	t.Parallel()

	// The hook this replaced matched `file -b` output against ELF and Mach-O.
	// All four of these were checked against that pattern and went through it;
	// git calls every one of them binary.
	for _, path := range []string{"logo.png", "archive.gz", "libfoo.a", "module.wasm"} {
		_, code := linesRun(t, "staged-binaries", "-\t-\t"+path+"\n")

		assert.Equal(t, 1, code, path)
	}
}

func TestLinesStagedBinaries_AllowsWhatTheAllowlistNames(t *testing.T) {
	t.Parallel()

	// A repository that legitimately tracks an image needs a way to say so,
	// or the guard gets disabled wholesale the first time it is inconvenient.
	out, code := linesRunAllowing(t, "staged-binaries", "-\t-\tdocs/logo.png\n", "docs/logo.png")

	assert.Equal(t, 0, code)
	assert.Empty(t, strings.TrimSpace(out))
}

func TestLinesStagedBinaries_AnAllowlistDoesNotCoverEverythingElse(t *testing.T) {
	t.Parallel()

	// The failure that would make the allowlist dangerous: allowing one path
	// must not wave the rest through.
	out, code := linesRunAllowing(t, "staged-binaries",
		"-\t-\tdocs/logo.png\n-\t-\tstray-bin\n", "docs/logo.png")

	assert.Equal(t, 1, code)
	assert.Contains(t, out, "stray-bin")
	assert.NotContains(t, out, "logo.png")
}
