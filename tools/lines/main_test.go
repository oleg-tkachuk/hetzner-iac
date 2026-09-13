// Package lines tests main.awk, the file beside it.
//
// The shared awk program is the one piece of this repository written in a
// language with no compiler and no type checker, so it is the piece most in
// need of a test. An awk program nobody tests is a shell pipeline with extra
// steps — and pipelines returning the wrong answer quietly is the problem the
// file was written to solve.
package lines

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

// run feeds stdin to main.awk with the given op and returns its output and
// exit code.
func run(t *testing.T, op, stdin string, files ...string) (string, int) {
	t.Helper()

	return runWith(t, op, "", stdin, files...)
}

// runAllowing is run with the staged-binaries allowlist set.
func runAllowing(t *testing.T, op, stdin, allow string) (string, int) {
	t.Helper()

	return runWith(t, op, allow, stdin)
}

func runWith(t *testing.T, op, allow, stdin string, files ...string) (string, int) {
	t.Helper()

	script := "main.awk"

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

func TestAvailMB_ReadsTheFirstDataRow(t *testing.T) {
	t.Parallel()

	// The shape df prints: a header, then the number with its unit suffix.
	out, code := run(t, "avail-mb", "Avail\n36109M\n")

	assert.Equal(t, 0, code)
	assert.Equal(t, "36109", strings.TrimSpace(out))
}

func TestAvailMB_IgnoresASecondFilesystem(t *testing.T) {
	t.Parallel()

	// A df given more than one path prints more than one row. Summing them
	// would report space that is not on the filesystem the caller asked about.
	out, code := run(t, "avail-mb", "Avail\n36109M\n99999M\n")

	assert.Equal(t, 0, code)
	assert.Equal(t, "36109", strings.TrimSpace(out))
}

func TestAvailMB_FailsWhenDfPrintsNothing(t *testing.T) {
	t.Parallel()

	// The improvement over `tail -1 | tr -dc '0-9'`, which returned an empty
	// string that arithmetic then treated as zero — reporting a full disk as
	// an empty one.
	out, code := run(t, "avail-mb", "Avail\n")

	assert.Equal(t, 1, code)
	assert.Contains(t, out, "no data row")
}

func TestCacheWriters_FindsExactlyOneInTheRealWorkflows(t *testing.T) {
	t.Parallel()

	// The guard this backs: an Actions cache key is immutable, so whichever
	// job saves first owns it, and the job that compiles the tree is by
	// definition slower than one that does not.
	workflows, err := filepath.Glob(filepath.Join("..", "..", ".github", "workflows", "*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, workflows)

	out, code := run(t, "cache-writers", "", workflows...)

	assert.Equal(t, 0, code)
	assert.Contains(t, out, "count=1")
}

func TestCacheWriters_DoesNotMatchItsOwnPattern(t *testing.T) {
	t.Parallel()

	// The shell version counted the string inside its own source on the first
	// attempt. The pattern is anchored to the start of a line for that reason,
	// so a mention in prose does not count.
	out, code := run(t, "cache-writers", "a comment about cache-mode: save\n")

	assert.Equal(t, 0, code)
	assert.Contains(t, out, "count=0")
}

func TestOp_IsRequiredAndChecked(t *testing.T) {
	t.Parallel()

	// Silently printing nothing for a mistyped op would reintroduce the exact
	// failure this file replaced.
	out, code := run(t, "", "x\n")
	assert.Equal(t, 2, code)
	assert.Contains(t, out, "op=<name> is required")

	out, code = run(t, "avail-megabytes", "x\n")
	assert.Equal(t, 2, code)
	assert.Contains(t, out, "unknown op")
}

func TestReverseWords_LastFirst(t *testing.T) {
	t.Parallel()

	// The layer list arrives as one space-separated string from the Taskfile,
	// so tokens rather than lines: piping it through `tr` first would be the
	// pipeline this op exists to remove.
	out, code := run(t, "reverse-words", "10-node-platform 30-core 40-ingress")

	assert.Equal(t, 0, code)
	assert.Equal(t, "40-ingress\n30-core\n10-node-platform", strings.TrimSpace(out))
}

func TestReverseWords_TakesSeveralLinesToo(t *testing.T) {
	t.Parallel()

	// Same answer whether the list arrives on one line or many, which is what
	// makes it a drop-in for `tac` and for `tail -r`.
	out, code := run(t, "reverse-words", "a b\nc\n")

	assert.Equal(t, 0, code)
	assert.Equal(t, "c\nb\na", strings.TrimSpace(out))
}

func TestReverseWords_EmptyInputIsEmptyOutput(t *testing.T) {
	t.Parallel()

	// Not an error: a layer list can legitimately be empty, and the loop that
	// consumes this then runs zero times.
	out, code := run(t, "reverse-words", "")

	assert.Equal(t, 0, code)
	assert.Empty(t, strings.TrimSpace(out))
}

func TestStagedBinaries_FailsAndNamesThem(t *testing.T) {
	t.Parallel()

	// The shape `git diff --cached --numstat` prints: counts for text, a pair
	// of dashes for anything git calls binary.
	out, code := run(t, "staged-binaries", "2\t0\tmain.go\n-\t-\tstray-bin\n-\t-\tlogo.png\n")

	assert.Equal(t, 1, code, "a staged binary must fail the commit")
	assert.Contains(t, out, "stray-bin")
	assert.Contains(t, out, "logo.png")
	// The remedy, because the cause is almost always the same one.
	assert.Contains(t, out, "go build")
}

func TestStagedBinaries_TextOnlyPasses(t *testing.T) {
	t.Parallel()

	out, code := run(t, "staged-binaries", "2\t0\tmain.go\n14\t3\tREADME.md\n")

	assert.Equal(t, 0, code)
	assert.Empty(t, strings.TrimSpace(out))
}

func TestStagedBinaries_NothingStagedPasses(t *testing.T) {
	t.Parallel()

	// An empty commit, or a commit of deletions only. Not a failure.
	_, code := run(t, "staged-binaries", "")

	assert.Equal(t, 0, code)
}

func TestStagedBinaries_CatchesWhatTheOldPatternMissed(t *testing.T) {
	t.Parallel()

	// The hook this replaced matched `file -b` output against ELF and Mach-O.
	// All four of these were checked against that pattern and went through it;
	// git calls every one of them binary.
	for _, path := range []string{"logo.png", "archive.gz", "libfoo.a", "module.wasm"} {
		_, code := run(t, "staged-binaries", "-\t-\t"+path+"\n")

		assert.Equal(t, 1, code, path)
	}
}

func TestStagedBinaries_AllowsWhatTheAllowlistNames(t *testing.T) {
	t.Parallel()

	// A repository that legitimately tracks an image needs a way to say so,
	// or the guard gets disabled wholesale the first time it is inconvenient.
	out, code := runAllowing(t, "staged-binaries", "-\t-\tdocs/logo.png\n", "docs/logo.png")

	assert.Equal(t, 0, code)
	assert.Empty(t, strings.TrimSpace(out))
}

func TestStagedBinaries_AnAllowlistDoesNotCoverEverythingElse(t *testing.T) {
	t.Parallel()

	// The failure that would make the allowlist dangerous: allowing one path
	// must not wave the rest through.
	out, code := runAllowing(t, "staged-binaries",
		"-\t-\tdocs/logo.png\n-\t-\tstray-bin\n", "docs/logo.png")

	assert.Equal(t, 1, code)
	assert.Contains(t, out, "stray-bin")
	assert.NotContains(t, out, "logo.png")
}
