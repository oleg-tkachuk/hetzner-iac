package ci

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dottedQuad matches an IPv4 address, and not the middle of a longer one: a
// version like 1.2.3.4.5 or a chart's 2.23.0 must not be read as an address.
//
// This is the check's own history. The first sweep for this used `git grep -E`
// with \b around the pattern, which POSIX extended regular expressions do not
// define — so it matched nothing and reported a clean tree. A gate whose first
// answer is a false negative is worse than no gate.
var dottedQuad = regexp.MustCompile(`(^|[^0-9.])((?:[0-9]{1,3}\.){3}[0-9]{1,3})([^0-9.]|$)`)

// documentationRanges are the addresses RFC 5737 reserves for exactly this:
// writing an example down. Anything in them is fine anywhere.
var documentationRanges = []string{"192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24"}

// publicAddressesWithAReason are the real ones this repository is allowed to
// name, and why.
//
// Both are third-party infrastructure quoted as measured evidence, in comments
// that would lose their point without the number — the whole argument of the
// first is that the address belongs to somebody else and is theirs to change.
// Neither says anything about this project's own cluster.
var publicAddressesWithAReason = map[string]string{
	"172.65.46.172":   "Cloudflare, fronting Let's Encrypt — the reason 50-allow-acme.yaml uses toFQDNs and not toCIDRSet",
	"213.239.246.78":  "what api.hetzner.cloud answers with, in the policy that allows reaching it",
	"1.1.1.1":         "a public resolver, as an example of one",
	"8.8.8.8":         "a public resolver, as an example of one",
	"255.255.255.255": "a mask, not a host",
}

// TestTrackedFiles_NameNoRealAddressOfTheirOwn keeps this repository from
// publishing where its own infrastructure is.
//
// Written for going public, and the case it was written from was a comment in
// internal/pkg/clustersmoke: it recorded a measured failure and named the
// project's own load-balancer address to do it. Not a credential, and the
// address is not even reservable — but it was the one place the repository
// pointed at a live host of its own, and the comment did not need it.
//
// Everything else already followed the convention internal/pkg/values states
// out loud: documentation ranges and obviously-placeholder names on purpose.
// This is that convention, checked.
func TestTrackedFiles_NameNoRealAddressOfTheirOwn(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	listed, err := exec.CommandContext(t.Context(), "git", "-C", root, "ls-files").Output()
	require.NoError(t, err)

	var documentation []*net.IPNet

	for _, cidr := range documentationRanges {
		_, network, parseErr := net.ParseCIDR(cidr)
		require.NoError(t, parseErr, cidr)

		documentation = append(documentation, network)
	}

	var checked int

	for _, name := range strings.Split(strings.TrimSpace(string(listed)), "\n") {
		if name == "" || name == filepath.Join("internal", "ci", "addresses_test.go") {
			continue
		}

		raw, readErr := os.ReadFile(filepath.Join(root, name))
		if readErr != nil {
			// A path git lists but the filesystem does not have is a symlink
			// or a submodule, neither of which holds text to read.
			continue
		}

		for _, match := range dottedQuad.FindAllStringSubmatch(string(raw), -1) {
			address := net.ParseIP(match[2])
			if address == nil || address.To4() == nil {
				continue
			}

			if address.IsPrivate() || address.IsLoopback() || address.IsUnspecified() ||
				address.IsMulticast() || address.IsLinkLocalUnicast() {
				continue
			}

			if inAny(address, documentation) {
				continue
			}

			checked++

			_, allowed := publicAddressesWithAReason[address.String()]
			assert.True(t, allowed,
				"%s names the real address %s. If it is this project's own, take it out — a "+
					"documentation range from RFC 5737 carries the same meaning. If it belongs "+
					"to somebody else and the point needs it, add it to "+
					"publicAddressesWithAReason with the reason",
				name, address)
		}
	}

	assert.Positive(t, checked,
		"no real address was examined, so this test proved nothing — the pattern is broken again")
}

func inAny(address net.IP, networks []*net.IPNet) bool {
	for _, network := range networks {
		if network.Contains(address) {
			return true
		}
	}

	return false
}
