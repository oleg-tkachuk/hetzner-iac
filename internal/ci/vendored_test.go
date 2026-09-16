package ci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// vendored is a file copied from upstream at a tag, with the URL it came from
// and the digest its body had when it was copied.
//
// Recorded here rather than beside each file because there is one list to read
// when asking "what did we copy in, from where, and as what".
//
// The digest is what makes the check work with no network. Print a new one for
// a deliberate update with:
//
//	go test ./internal/ci -run TestVendored_MatchesItsRecordedDigest -v
var vendored = []struct {
	path   string
	url    string
	digest string
}{
	{
		path: "../../layers/30-cluster-services/manifests/kubelet-serving-cert-approver.yaml",
		url: "https://raw.githubusercontent.com/alex1989hu/kubelet-serving-cert-approver/" +
			"v0.12.0/deploy/standalone-install.yaml",
		digest: "3ec71eeb521b1f32b015f3d9a8972744dbcfabae0b7cc73c1856a5abdb3fa776",
	},
}

// TestVendored_MatchesItsRecordedDigest is the half that needs no network, and
// it is the half that was missing.
//
// The check below fetches upstream and SKIPS when it cannot, which was a
// deliberate choice — a gate that goes red because github.com is unreachable
// is a gate people switch off. But a skip is a pass: in a runner with no
// egress, or during the twenty minutes when GitHub's own
// downloads returned 504, a hand edit to this file would have gone through
// with the suite green and nothing said.
//
// A digest recorded in the repository separates the two questions. Has this
// copy been edited since it was vendored? — answerable here, offline, and so
// this test FAILS rather than skips. Has upstream changed at that tag? — that
// genuinely needs the network, and that is the one allowed to skip.
//
// carvel-dev/vendir was the obvious tool for this and does not do it: for an
// `http` source its lock file records `http: {}` — no digest and no resolved
// ref — measured on 0.46.2. Its sync also replaces the managed directory
// wholesale, which would strip the Apache-2.0 provenance header this copy
// carries for redistribution.
func TestVendored_MatchesItsRecordedDigest(t *testing.T) {
	t.Parallel()

	for _, file := range vendored {
		local, err := os.ReadFile(file.path)
		require.NoError(t, err, file.path)

		got := digest(afterHeader(string(local)))

		// Printed on every run, so a deliberate update has the value to paste
		// without anyone computing a sha256 by hand.
		t.Logf("%s\n  recorded %s\n  actual   %s", file.path, file.digest, got)

		assert.Equal(t, file.digest, got,
			"%s has been edited since it was vendored from %s.\n"+
				"If the edit is deliberate, update the tag, the header and the digest above.",
			file.path, file.url)
	}
}

// TestVendored_MatchesUpstreamAtItsTag catches a hand edit to a copied file.
//
// The approver manifest grants a controller permission to approve certificate
// signing requests — the thing that decides which kubelet serving certificates
// the cluster CA signs. A quiet change to its RBAC is not something to find out
// about later, and a vendored file has no upstream to diff against unless
// something fetches it.
//
// Skipped rather than failed when the network is unavailable: this repository
// has spent a day with intermittent access to github.com, and a check that
// goes red for that reason is one people switch off.
// upstreamCheck asks for the half of this gate that needs the network. The
// nightly workflow sets it; a unit suite does not, which is why `go test` is
// offline and takes no 30-second timeout per vendored file.
const upstreamCheck = "CI_CHECK_UPSTREAM"

// TestVendored_MatchesUpstreamAtItsTag is the half that can only be answered
// by asking upstream, and it runs when something asks for it.
//
// It used to run always and skip when the fetch failed, which is the shape
// this repository refuses everywhere else: a gate that skips itself when a
// dependency is missing is a gate that quietly stops running. So the skip is
// now about whether it was ASKED for, and a fetch that then fails is a
// failure.
func TestVendored_MatchesUpstreamAtItsTag(t *testing.T) {
	t.Parallel()

	if os.Getenv(upstreamCheck) == "" {
		t.Skipf("%s is unset: this half asks upstream, and the nightly run is where that belongs",
			upstreamCheck)
	}

	for _, file := range vendored {
		local, err := os.ReadFile(file.path)
		require.NoError(t, err, file.path)

		// The provenance header this repository adds is not upstream's, so the
		// comparison starts at the first line that is.
		body := afterHeader(string(local))

		upstream, err := fetch(t, file.url)
		require.NoError(t, err, "%s was asked for and could not be reached", file.url)

		// Against the recorded digest as well as the file, so a run with
		// network answers both questions: whether the copy drifted, and
		// whether the record itself is still what upstream serves.
		assert.Equal(t, digest(upstream), file.digest,
			"%s serves something other than the digest recorded for it. Upstream re-tagged, "+
				"or the record is wrong — either way this is not a local edit",
			file.url)

		assert.Equal(t, digest(upstream), digest(body),
			"%s no longer matches %s — if the edit is deliberate, update the tag and the header",
			file.path, file.url)
	}
}

// afterHeader drops the leading comment block this repository prepends.
func afterHeader(body string) string {
	lines := strings.Split(body, "\n")

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			return strings.Join(lines[i:], "\n")
		}
	}

	return body
}

func digest(body string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(body)))

	return hex.EncodeToString(sum[:])
}

func fetch(t *testing.T, url string) (string, error) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return "", assert.AnError
	}

	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return "", err
	}

	return string(raw), nil
}

func TestAfterHeader_DropsOnlyOurProvenanceBlock(t *testing.T) {
	t.Parallel()

	// The header this repository prepends is not upstream's, so it has to come
	// off before comparing — but only it. A comment that is part of upstream's
	// own file must survive, or every vendored file with a licence header
	// would compare unequal for ever.
	body := afterHeader("# ours: where this came from\n#\n# and why\n\napiVersion: v1\nkind: Namespace\n")

	assert.Equal(t, "apiVersion: v1\nkind: Namespace\n", body)
	assert.NotContains(t, body, "ours")
}
