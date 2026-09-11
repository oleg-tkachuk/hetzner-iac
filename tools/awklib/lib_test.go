// Package awklib tests awk/lib.awk.
//
// The shared awk program is the one piece of this repository written in a
// language with no compiler and no type checker, so it is the piece most in
// need of a test. An awk program nobody tests is a shell pipeline with extra
// steps — and pipelines returning the wrong answer quietly is the problem the
// file was written to solve.
package awklib_test

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

// run feeds stdin to awk/lib.awk with the given op and returns its output and
// exit code.
func run(t *testing.T, op, stdin string, files ...string) (string, int) {
	t.Helper()

	script := filepath.Join("..", "..", "awk", "lib.awk")

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	args := append([]string{"-f", script, "-v", "op=" + op}, files...)
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

func TestCountRows_ZeroIsAnAnswerNotAFailure(t *testing.T) {
	t.Parallel()

	// `grep -c .` exits 1 on zero, which needs a `|| true` under `set -e` and
	// makes a real failure look like an empty result.
	out, code := run(t, "count-rows", "")

	assert.Equal(t, 0, code)
	assert.Equal(t, "0", strings.TrimSpace(out))
}

func TestCountRows_SkipsBlankLines(t *testing.T) {
	t.Parallel()

	out, code := run(t, "count-rows", "a\n\nb\n   \nc\n")

	assert.Equal(t, 0, code)
	assert.Equal(t, "3", strings.TrimSpace(out))
}

func TestAnyRow_ExitStatusMirrorsGrepQ(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		stdin string
		want  int
	}{
		"empty":       {"", 1},
		"blank lines": {"\n   \n", 1},
		"one row":     {"x\n", 0},
	} {
		_, code := run(t, "any-row", tc.stdin)
		assert.Equal(t, tc.want, code, name)
	}
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
