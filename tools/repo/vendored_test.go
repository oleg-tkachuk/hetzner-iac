package repo

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

// vendored is a file copied from upstream at a tag, with the URL it came from.
//
// Recorded here rather than beside each file because there is one list to read
// when asking "what did we copy in, and from where".
var vendored = []struct {
	path string
	url  string
}{
	{
		path: "../../layers/30-cluster-services/manifests/kubelet-serving-cert-approver.yaml",
		url: "https://raw.githubusercontent.com/alex1989hu/kubelet-serving-cert-approver/" +
			"v0.12.0/deploy/standalone-install.yaml",
	},
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
func TestVendored_MatchesUpstreamAtItsTag(t *testing.T) {
	t.Parallel()

	for _, file := range vendored {
		local, err := os.ReadFile(file.path)
		require.NoError(t, err, file.path)

		// The provenance header this repository adds is not upstream's, so the
		// comparison starts at the first line that is.
		body := afterHeader(string(local))

		upstream, err := fetch(t, file.url)
		if err != nil {
			t.Skipf("cannot reach %s: %v", file.url, err)
		}

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

func TestDigest_IgnoresTrailingWhitespaceOnly(t *testing.T) {
	t.Parallel()

	// A trailing newline is what an editor adds and what a raw fetch may not
	// have; anything else is a real difference.
	assert.Equal(t, digest("kind: Namespace\n"), digest("kind: Namespace"))
	assert.NotEqual(t, digest("kind: Namespace"), digest("kind: Secret"))
}
