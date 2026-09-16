package ci

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

	listed, err := exec.CommandContext(t.Context(), "git", "-C", root, "ls-files").Output()
	require.NoError(t, err)

	var domains, emails int

	for _, name := range strings.Split(strings.TrimSpace(string(listed)), "\n") {
		if name == "" || name == filepath.Join("internal", "ci", "domains_test.go") {
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
