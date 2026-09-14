package pulumilog

import (
	"regexp"
	"strconv"
	"strings"
)

// Redacted is what replaces a secret. Distinctive on purpose: an operator who
// sees it should know a value was removed rather than wonder what produced an
// odd-looking string.
const Redacted = "[redacted]"

// hetznerTokenLength is the length of a Hetzner Cloud API token.
const hetznerTokenLength = 64

// redaction is one pattern and what to put in place of what it matched.
//
// Shape rather than a list of known values, because the values are Pulumi
// secret Outputs that this package never sees resolved — and because the leak
// worth guarding is the one nobody predicted. Each pattern is a marker that
// cannot plausibly appear in a legitimate log line from this repository.
var redactions = []struct {
	pattern *regexp.Regexp
	with    string
}{
	{
		// A PEM block: private keys, certificates, the Talos CA. The whole
		// block, not the header, because a multi-line detail would otherwise
		// leave the body behind.
		pattern: regexp.MustCompile(`(?s)-----BEGIN [^-]+-----.*?-----END [^-]+-----`),
		with:    Redacted,
	},
	{
		// kubeconfig and Secret fields whose value IS the credential. Named
		// exactly, so a key called `data` somewhere harmless is untouched.
		//
		// The key and the separator are kept: a line reading
		// `token: [redacted]` still tells the operator which field was
		// involved, which is most of why the line existed.
		pattern: regexp.MustCompile(`(?i)\b(client-key-data|client-certificate-data|` +
			`certificate-authority-data|token|password|secret)\b(\s*[:=]\s*)\S+`),
		with: "${1}${2}" + Redacted,
	},
	{
		// Credentials in a URL's userinfo — how a registry or an S3 endpoint
		// carries them. The scheme and host stay; only the userinfo goes.
		pattern: regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/\s:@]+:[^/\s@]+@`),
		with:    "${1}" + Redacted + "@",
	},
}

// tokenShape matches a run of exactly hetznerTokenLength letters and digits.
//
// Length only. Whether such a run is a token is decided in Go below, because
// RE2 has no lookahead and the condition that matters cannot be written as a
// pattern.
var tokenShape = regexp.MustCompile(`\b[A-Za-z0-9]{` + strconv.Itoa(hetznerTokenLength) + `}\b`)

// redact removes anything that looks like a credential from a log line.
//
// Every line this package emits goes through it, which is the point: a caller
// deciding per site whether its detail is sensitive is a caller that will get
// it wrong once. The cost is a few regexp matches per line, next to a gRPC
// call to the Pulumi engine.
//
// This is a backstop, not a licence. The right handling for a value known to
// be secret is not to log it — a redacted line still says a secret was in
// scope at that point in the code.
func redact(line string) string {
	for _, r := range redactions {
		line = r.pattern.ReplaceAllString(line, r.with)
	}

	return tokenShape.ReplaceAllStringFunc(line, func(match string) string {
		if !looksLikeToken(match) {
			return match
		}

		return Redacted
	})
}

// looksLikeToken separates a Hetzner API token from the other 64-character run
// this repository prints on purpose.
//
// A token is mixed case with digits; a sha256 digest is 64 lowercase hex
// characters, and the vendored manifest's recorded digest is printed by its own
// test. Redacting that would break a check while looking like a security
// improvement — so an uppercase letter AND a digit are both required.
func looksLikeToken(candidate string) bool {
	return strings.ContainsFunc(candidate, func(r rune) bool { return r >= 'A' && r <= 'Z' }) &&
		strings.ContainsFunc(candidate, func(r rune) bool { return r >= '0' && r <= '9' })
}
