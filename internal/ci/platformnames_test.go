package ci

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// platformConstant matches a constant in internal/pkg/platform, string or not.
var platformConstant = regexp.MustCompile(`(?m)^\s*(?:const\s+)?([A-Z][A-Za-z]*)\s*=\s*\S`)

// ownConstant matches a file declaring a constant, capturing what it is set
// to — because reading platform's own value is the right thing to do, and
// only a second literal is the problem.
var ownConstant = regexp.MustCompile(`(?m)^\s*(?:const\s+)?([A-Z][A-Za-z]*)\s*=\s*(.+)$`)

// TestPlatformNames_AreNotDeclaredTwice is the generalisation of how
// platform.IssuerName was found.
//
// That package exists for one reason, in its own words: a value two layers
// must spell identically cannot be a literal in each. `letsencrypt` was a
// constant in 30-cluster-services, which creates the ClusterIssuer, AND in
// 50-gitops, which annotates an Ingress with it — with a comment in the second
// saying it had to match the first. Nothing compared them, and one rename away
// was the failure IngressClass had already had: an Ingress naming an issuer
// that does not exist is accepted, and the Certificate sits pending with no
// event saying why.
//
// So: whatever platform NAMES, nothing else may name again.
//
// By name and not by value, and that is a decision this test made after
// getting it wrong. Comparing values reported layers/40-ingress declaring
// `Chart = "traefik"` against platform's `IngressClass = "traefik"` — a chart
// registry key and a Kubernetes IngressClass name, two different things that
// happen to share a word and are under no obligation to keep sharing it. A
// value rule needs an exemption list that grows with every coincidence, and a
// gate whose first finding is its own false positive is one everybody learns
// to skip. The failure this exists for had the same NAME in both places.
func TestPlatformNames_AreNotDeclaredTwice(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	raw, err := os.ReadFile(filepath.Join(root, "internal", "pkg", "platform", "platform.go"))
	require.NoError(t, err)

	named := map[string]bool{}

	for _, match := range platformConstant.FindAllStringSubmatch(string(raw), -1) {
		named[match[1]] = true
	}

	require.NotEmpty(t, named, "platform names nothing; this test is checking nothing")

	var checked int

	for _, dir := range []string{"layers", "infra", "internal/pkg", "tools"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}

			// platform itself declares them; a test may assert on one.
			if entry.IsDir() || !strings.HasSuffix(path, ".go") ||
				strings.HasSuffix(path, "_test.go") ||
				strings.Contains(filepath.ToSlash(path), "internal/pkg/platform/") {
				return nil
			}

			body, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}

			checked++

			for _, match := range ownConstant.FindAllStringSubmatch(string(body), -1) {
				if !named[match[1]] {
					continue
				}

				// An alias reading platform's own constant is the point, not a
				// breach: it cannot drift. layers/10-node-platform re-exports
				// StorageClass exactly that way, and flagging it was this
				// test's second false positive.
				if strings.Contains(match[2], "platform.") {
					continue
				}

				assert.Fail(t, "a platform name is declared twice",
					"%s sets %s to %s of its own, and internal/pkg/platform already names it. "+
						"Read platform's instead — that package exists because a value two "+
						"layers must spell identically cannot be a literal in each",
					relativeToRoot(path), match[1], strings.TrimSpace(match[2]))
			}

			return nil
		})
		require.NoError(t, err)
	}

	assert.Positive(t, checked, "no Go file was examined; the walk is broken")
}
