package pulumilog

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// token is a value of the shape a Hetzner API token has: exactly 64 letters
// and digits, mixed case. Generated from the constant so the fixture cannot
// drift from the pattern.
func token(t *testing.T) string {
	t.Helper()

	const alphabet = "AbCdEf0123456789"

	var sb strings.Builder
	for sb.Len() < hetznerTokenLength {
		sb.WriteByte(alphabet[sb.Len()%len(alphabet)])
	}

	got := sb.String()
	require.Len(t, got, hetznerTokenLength)

	return got
}

func TestRedact(t *testing.T) {
	t.Parallel()

	secret := token(t)

	tests := []struct {
		name string
		line string
		want string
	}{
		{
			name: "an ordinary line is untouched",
			line: "◉ ingress · traefik · chart traefik 41.5.0 → traefik",
			want: "◉ ingress · traefik · chart traefik 41.5.0 → traefik",
		},
		{
			// The failure this exists for: a token interpolated into a detail
			// reaches Pulumi's diagnostics, which are kept in the update's
			// history.
			name: "an api token is removed",
			line: "◉ core · token · using " + secret,
			want: "◉ core · token · using " + Redacted,
		},
		{
			// The false positive that would have been worse than the gap. A
			// sha256 digest is 64 characters too, and this repository prints
			// one on purpose.
			name: "a sha256 digest is not a token",
			line: "ok  3ec71eeb521b1f32b015f3d9a8972744dbcfabae0b7cc73c1856a5abdb3fa776",
			want: "ok  3ec71eeb521b1f32b015f3d9a8972744dbcfabae0b7cc73c1856a5abdb3fa776",
		},
		{
			name: "a pem block goes entirely, not just its header",
			line: "▲ core · ca · -----BEGIN RSA PRIVATE KEY-----\nMIIEow\nIBAAKC\n-----END RSA PRIVATE KEY-----",
			want: "▲ core · ca · " + Redacted,
		},
		{
			// The key survives, because it is most of why the line existed.
			name: "a kubeconfig credential keeps its key",
			line: "○ core · kubeconfig · client-key-data: LS0tLS1CRUdJTiBS",
			want: "○ core · kubeconfig · client-key-data: " + Redacted,
		},
		{
			name: "an equals separator is kept as written",
			line: "◉ core · env · HCLOUD_TOKEN=" + secret,
			want: "◉ core · env · HCLOUD_TOKEN=" + Redacted,
		},
		{
			name: "a password field is removed whatever its case",
			line: "▲ backup · s3 · Password: hunter2",
			want: "▲ backup · s3 · Password: " + Redacted,
		},
		{
			// A registry or an S3 endpoint carries credentials this way. The
			// host is the useful half and stays.
			name: "url userinfo goes, the host stays",
			line: "◉ backup · endpoint · https://key:supersecret@fsn1.your-objectstorage.com",
			want: "◉ backup · endpoint · https://" + Redacted + "@fsn1.your-objectstorage.com",
		},
		{
			name: "a bare domain is not userinfo",
			line: "◉ gitops · ingress · domain argocd.example.test",
			want: "◉ gitops · ingress · domain argocd.example.test",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, test.want, redact(test.line))
		})
	}
}

// TestLine_RedactsWhateverTheCallerPassed is the part that matters more than
// the patterns: the guard is on the one function every line goes through, so
// no call site can route around it.
func TestLine_RedactsWhateverTheCallerPassed(t *testing.T) {
	t.Parallel()

	secret := token(t)
	logger := &Logger{scope: "core", colour: false}

	line := logger.line(GlyphRunning, "", "token", "reading "+secret)

	assert.NotContains(t, line, secret, "a secret in the detail reached the log line")
	assert.Contains(t, line, Redacted)

	// And the rest of the line is intact, so the redaction is not a blunt
	// instrument that costs the message.
	assert.Contains(t, line, "core · token · reading")
}
