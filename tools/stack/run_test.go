package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optlist"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errBoom = errors.New("boom")

// fakeLister answers a listing the way the backend would.
type fakeLister struct {
	summaries []auto.StackSummary
	err       error
}

func (f fakeLister) ListStacks(context.Context, ...optlist.Option) ([]auto.StackSummary, error) {
	return f.summaries, f.err
}

func testDeps(lister stackLister, openErr error, out *bytes.Buffer) deps {
	return deps{
		requireLogin: func([]string) error { return nil },
		open:         func(context.Context, string) (stackLister, error) { return lister, openErr },
		out:          out,
	}
}

func TestRun_ListsTheBackendsStacksBesideTheTopologies(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cluster.dev.yaml"), []byte(sparse), 0o600))

	var out bytes.Buffer

	lister := fakeLister{summaries: []auto.StackSummary{{Name: "prod", ResourceCount: counted(7)}}}

	require.NoError(t, run(context.Background(), []string{"list", dir}, testDeps(lister, nil, &out)))

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	require.Len(t, lines, 3, out.String())
	assert.True(t, strings.HasPrefix(lines[0], "STACK"))
	assert.Contains(t, out.String(), "hel1", "the topology half")
	assert.Contains(t, out.String(), "        7 ", "the backend half")
}

func TestRun_ListNamesWhatItCouldNotRead(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	for name, tc := range map[string]struct {
		d       func(*bytes.Buffer) deps
		wantErr string
	}{
		"login": {func(out *bytes.Buffer) deps {
			d := testDeps(fakeLister{}, nil, out)
			d.requireLogin = func([]string) error { return errBoom }

			return d
		}, "boom"},
		"open": {func(out *bytes.Buffer) deps { return testDeps(nil, errBoom, out) },
			"open " + dir + " as a pulumi workspace: boom"},
		"list": {func(out *bytes.Buffer) deps { return testDeps(fakeLister{err: errBoom}, nil, out) },
			"list stacks in " + dir + ": boom"},
	} {
		var out bytes.Buffer

		err := run(context.Background(), []string{"list", dir}, tc.d(&out))
		require.Error(t, err, name)
		assert.Equal(t, tc.wantErr, err.Error(), name)
		assert.Empty(t, out.String(), name)
	}

	var out bytes.Buffer

	absent := filepath.Join(dir, "absent")
	err := run(context.Background(), []string{"list", absent}, testDeps(fakeLister{}, nil, &out))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "look for topologies in "+absent)
	assert.Empty(t, out.String())
}
