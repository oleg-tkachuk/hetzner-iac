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

	// tasklibInclude is one remote taskfile an entry point includes.
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
//
// Both entry points are read, and holding their pins equal is the other half.
// The pin has to be written twice — Task resolves `includes:` before it loads
// `dotenv:`, and an included taskfile may not declare `dotenv:` at all, so
// there is no third file both can read it from. A bump applied to one leaves
// the other fetching a ref whose checksums were just replaced, and the message
// is about trust rather than about the version.
func TestTasklib_EveryRemoteTaskfileHasItsChecksum(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	entryPoints := []string{"Taskfile.yaml", devTaskfile}

	expected := map[string]string{}

	var pinned string

	for _, name := range entryPoints {
		raw, err := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, err, name)

		pin := tasklibPin.FindStringSubmatch(string(raw))
		require.NotNil(t, pin, "%s declares no TASKLIB", name)

		if pinned == "" {
			pinned = pin[1]
		}

		require.Equal(t, pinned, pin[1],
			"%s pins the task library at a different ref from %s. Both entry points fetch "+
				"the same modules, and the checksums under %s are named for the URL — so one "+
				"of them is about to be refused as untrusted",
			name, entryPoints[0], remoteCache)

		if !strings.HasPrefix(pinned, "http") {
			// The local form, for working on the library itself. Nothing is
			// downloaded, so nothing needs trusting.
			t.Logf("TASKLIB is the local form %q; no remote taskfile to trust", pinned)

			return
		}

		includes := tasklibInclude.FindAllStringSubmatch(string(raw), -1)
		require.NotEmpty(t, includes, "%s includes no remote taskfile", name)

		for _, match := range includes {
			module := match[1]
			remote := fmt.Sprintf(pinned, module)

			parsed, parseErr := url.Parse(remote)
			require.NoError(t, parseErr, remote)

			// Task names the file for the sha256 of the whole URL, which is
			// why the ref is part of it and why a bump invalidates every one.
			sum := sha256.Sum256([]byte(remote))
			expected[fmt.Sprintf("git.%s.%s.%s.checksum",
				parsed.Host, module, hex.EncodeToString(sum[:]))] = remote
		}
	}

	present := map[string]bool{}

	cached, err := os.ReadDir(filepath.Join(root, remoteCache))
	require.NoError(t, err, "%s is missing, so no remote taskfile is trusted", remoteCache)

	for _, entry := range cached {
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
