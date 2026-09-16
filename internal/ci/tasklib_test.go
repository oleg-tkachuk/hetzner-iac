package ci

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	// tasklibPin is the shared task library's URL, ref and all.
	tasklibPin = regexp.MustCompile(`(?m)^\s*TASKLIB:\s*'([^']+)'`)

	// tasklibInclude is one remote taskfile the root file includes.
	tasklibInclude = regexp.MustCompile(`{{printf \.TASKLIB "([a-z0-9-]+)"}}`)
)

// remoteCache is where Task keeps what it fetched, and where the checksum
// lockfiles this repository commits live.
const remoteCache = ".task/remote"

// TestTasklib_EveryRemoteTaskfileHasItsChecksum is the gate for a CI failure
// that reads like a permissions problem and is a missing lockfile.
//
// Task refuses a remote taskfile it has not been told to trust:
//
//	task: Taskfile "https://…/taskfiles.git//go?ref=v7.1.0" not trusted by user
//
// What tells it is a checksum file under .task/remote, named for the sha256 of
// the URL — ref included. So bumping the ref invalidates all four at once, and
// a commit that moves the pin without swapping them fails every job that runs
// a task, with a message about trust rather than about the bump.
//
// It has happened twice: once mid-session, and once as PR #178, which changed
// Taskfile.yaml alone.
func TestTasklib_EveryRemoteTaskfileHasItsChecksum(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	raw, err := os.ReadFile(filepath.Join(root, "Taskfile.yaml"))
	require.NoError(t, err)

	pin := tasklibPin.FindStringSubmatch(string(raw))
	require.NotNil(t, pin, "Taskfile.yaml declares no TASKLIB")

	template := pin[1]
	if !strings.HasPrefix(template, "http") {
		// The local form, for working on the library itself. Nothing is
		// downloaded, so nothing needs trusting.
		t.Logf("TASKLIB is the local form %q; no remote taskfile to trust", template)

		return
	}

	names := tasklibInclude.FindAllStringSubmatch(string(raw), -1)
	require.NotEmpty(t, names, "Taskfile.yaml includes no remote taskfile")

	expected := map[string]string{}

	for _, match := range names {
		name := match[1]
		remote := fmt.Sprintf(template, name)

		parsed, parseErr := url.Parse(remote)
		require.NoError(t, parseErr, remote)

		// Task names the file for the sha256 of the whole URL, which is why
		// the ref is part of it and why a bump invalidates every one.
		sum := sha256.Sum256([]byte(remote))
		expected[fmt.Sprintf("git.%s.%s.%s.checksum",
			parsed.Host, name, hex.EncodeToString(sum[:]))] = remote
	}

	present := map[string]bool{}

	entries, err := os.ReadDir(filepath.Join(root, remoteCache))
	require.NoError(t, err, "%s is missing, so no remote taskfile is trusted", remoteCache)

	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".checksum") {
			present[entry.Name()] = true
		}
	}

	for name, remote := range expected {
		assert.True(t, present[name],
			"%s/%s is missing, so Task will refuse %s as untrusted. Run a task with --yes "+
				"once and commit the new .checksum files in the same commit as the pin",
			remoteCache, name, remote)
	}

	// And nothing left over: a lockfile for a ref no longer pinned is a stale
	// trust decision, and it is how a directory of them grows one per bump.
	var stale []string

	for name := range present {
		if _, wanted := expected[name]; !wanted {
			stale = append(stale, name)
		}
	}

	sort.Strings(stale)

	assert.Empty(t, stale,
		"%s holds %d checksum file(s) for a ref that is no longer pinned: %s",
		remoteCache, len(stale), strings.Join(stale, ", "))
}
