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

// This file is one question asked twice: does a tracked file name something
// real that belongs to this project — an address, a domain, an operator's
// address — where a reserved placeholder would have carried the same meaning?
//
// It was two files, and they had the same shape down to the line: walk
// `git ls-files`, read each, match a pattern, forgive what a standard reserves
// for documentation. The walk is shared now; the rules are not, because an
// address and a name are refused for different reasons and the messages have
// to say which.
//
// Both were written for going public, and both found something.

// tracked is every file git knows about, which is what "published" means.
//
// Read from git rather than walked from disk on purpose: the gitignored files
// are the ones holding an operator's real values — the topology with their
// admin CIDR, the stack config with their token — and a sweep that read those
// would refuse the very files that are allowed to hold them.
func tracked(t *testing.T, root string) []string {
	t.Helper()

	listed, err := exec.CommandContext(t.Context(), "git", "-C", root, "ls-files").Output()
	require.NoError(t, err)

	return strings.Split(strings.TrimSpace(string(listed)), "\n")
}

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

	var documentation []*net.IPNet

	for _, cidr := range documentationRanges {
		_, network, parseErr := net.ParseCIDR(cidr)
		require.NoError(t, parseErr, cidr)

		documentation = append(documentation, network)
	}

	var checked int

	for _, name := range tracked(t, root) {
		if name == "" || name == filepath.Join("internal", "ci", "identifiers_test.go") {
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

// domainAssignment matches a value written for one of the two fields that
// carry a cluster's public name — `metadata.domain` and `metadata.dnsZone` —
// in any spelling this repository uses them: yaml, a Go field, a documented
// example, a commented-out line in the topology template.
//
// Deliberately an ASSIGNMENT and not every domain-shaped token in the tree.
// The broader formulation was written first and rejected: `api.hetzner.cloud`,
// `docs.siderolabs.com` and two dozen more are third-party names this
// repository is right to print, so the check became an allowlist of other
// people's infrastructure — and one that matched `cluster.example.yaml` and
// `main.go` as domains until it carried a list of file extensions too. What is
// worth checking is narrower and exact: a value a reader could paste into a
// topology or a config as the name of THIS cluster.
// The case-insensitive group covers the key alone. `(?i)` over the whole
// pattern was the first form and it read `Domain: stack.GetStringOutput` as a
// domain, because a case-insensitive [a-z] matches G.
var domainAssignment = regexp.MustCompile(
	`(?i:dns_?zone|domain)["']?\s*[:=]\s*["']?([a-z0-9][a-z0-9.\-]*\.[a-z][a-z0-9\-]*)`)

// emailAddress matches the ACME contact, the other value that is somebody's
// own. The leading class keeps it off a URL's userinfo — `https://key:secret@
// host` is a credential in a redaction fixture, not an address.
var emailAddress = regexp.MustCompile(
	`(^|[^:/a-z0-9._%+\-])([a-z0-9][a-z0-9._%+\-]*)@([a-z0-9][a-z0-9.\-]*\.[a-z][a-z0-9\-]*)`)

// reservedTLDs are the names RFC 2606 §2 sets aside for documentation and
// testing. A value under any of them cannot be anybody's real cluster.
var reservedTLDs = []string{".test", ".example", ".invalid", ".localhost"}

// reservedLabel is RFC 2606 §3's `example.com`, `example.net` and
// `example.org` generalised to the label they share, which is what makes a
// name obviously an example. Matching the label rather than the three
// registered names also accepts `platform.example.co.uk` — the fixture that
// exists to show a zone cut is not the last two labels, and no more real than
// the others.
const reservedLabel = "example"

// TestTrackedFiles_NameNoDomainOfTheirOwn keeps this repository from
// publishing the name of the cluster it was written for.
//
// The sibling of TestTrackedFiles_NameNoRealAddressOfTheirOwn, and written for
// the same reason: the repository is public, the domain is an operator's own,
// and the documentation needs to show the fields anyway. Both are answered by
// writing the examples in reserved namespace — which every example here
// already did, so this is that convention checked rather than a repair.
//
// What it does NOT check is prose: a third-party hostname in a sentence or a
// link is out of scope by construction. See domainAssignment for why.
func TestTrackedFiles_NameNoDomainOfTheirOwn(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	var domains, emails int

	for _, name := range tracked(t, root) {
		if name == "" || name == filepath.Join("internal", "ci", "identifiers_test.go") {
			continue
		}

		raw, readErr := os.ReadFile(filepath.Join(root, name))
		if readErr != nil {
			// A path git lists but the filesystem does not have is a symlink
			// or a submodule, neither of which holds text to read.
			continue
		}

		text := string(raw)

		for _, match := range domainAssignment.FindAllStringSubmatch(text, -1) {
			domains++

			assert.True(t, reservedName(match[1]),
				"%s writes %q as a domain. Examples belong in reserved namespace — "+
					"example.com, example.net, example.org or a .test name, per RFC 2606 — "+
					"and a real environment's name belongs in the gitignored "+
					"cluster.<stack>.yaml, not in a tracked file",
				name, match[1])
		}

		for _, match := range emailAddress.FindAllStringSubmatch(text, -1) {
			emails++

			assert.True(t, reservedName(match[3]),
				"%s writes the address %s@%s. An ACME contact is somebody's own — "+
					"use a reserved name from RFC 2606 for the example",
				name, match[2], match[3])
		}
	}

	assert.Positive(t, domains,
		"no domain assignment was examined, so this test proved nothing — the pattern is broken")
	assert.Positive(t, emails,
		"no address was examined, so this test proved nothing — the pattern is broken")
}

// reservedName reports whether a name is one RFC 2606 sets aside.
func reservedName(name string) bool {
	name = strings.ToLower(strings.TrimSuffix(name, "."))

	for _, tld := range reservedTLDs {
		if strings.HasSuffix(name, tld) {
			return true
		}
	}

	for _, label := range strings.Split(name, ".") {
		if label == reservedLabel {
			return true
		}
	}

	return false
}
