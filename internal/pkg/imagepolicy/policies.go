package imagepolicy

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"text/template"

	"github.com/google/go-containerregistry/pkg/name"
	"k8s.io/apimachinery/pkg/util/validation"
)

// Mode is what a ClusterImagePolicy does with an image that fails it.
type Mode string

// The two modes, in policy-controller's spelling.
const (
	// ModeWarn admits a pod that fails the policy and returns a warning.
	ModeWarn Mode = "warn"
	// ModeEnforce rejects it.
	ModeEnforce Mode = "enforce"
)

// NoMatchPolicy is what policy-controller does with an image no policy names,
// in its own spelling, beside this mode: warn beside warn, deny beside
// enforce. An image outside the inventory is held to the same standard as one
// inside it.
func (m Mode) NoMatchPolicy() string {
	if m == ModeEnforce {
		return noMatchDeny
	}

	return noMatchWarn
}

// The no-match-policy values the two modes map to.
const (
	noMatchWarn = "warn"
	noMatchDeny = "deny"
)

// The public-good Sigstore instance a key-signed publisher records in, and the
// digest a key signs over when a publisher does not say otherwise — cosign's.
const (
	PublicRekor          = "https://rekor.sigstore.dev"
	DefaultHashAlgorithm = "sha256"
)

// policyPrefix starts every generated policy's name, so the policies this
// inventory owns are told apart from any a cluster's user adds.
const policyPrefix = "platform-image-"

//go:embed clusterimagepolicies.yaml.tmpl
var policiesTemplate string

// policy is one ClusterImagePolicy's data: what the template interpolates.
type policy struct {
	Name  string
	Globs []string

	// Pin is set for an unsigned repository; Signer otherwise.
	Pin    *Pin
	Signer *Signer

	// For a key signer: the key, the digest it signed over, and the log its
	// signatures are recorded in — empty for a publisher that records none.
	KeyPEM          string
	HashAlgorithm   string
	TransparencyLog string
}

// Policies renders a ClusterImagePolicy for every repository in the
// inventory, as one multi-document YAML stream, in the given mode.
func (inv *Inventory) Policies(mode Mode) (string, error) {
	if mode != ModeWarn && mode != ModeEnforce {
		return "", fmt.Errorf("policy mode %q is neither %q nor %q", mode, ModeWarn, ModeEnforce)
	}

	policies := make([]policy, 0, len(inv.Images))

	for _, image := range inv.Images {
		p, err := policyFor(image)
		if err != nil {
			return "", fmt.Errorf("%s: %w", image.Repository, err)
		}

		policies = append(policies, p)
	}

	parsed, err := template.New("policies").Funcs(template.FuncMap{
		"json":   quote,
		"indent": indent,
	}).Option("missingkey=error").Parse(policiesTemplate)
	if err != nil {
		return "", fmt.Errorf("policy template: %w", err)
	}

	var out bytes.Buffer
	if err := parsed.Execute(&out, struct {
		Mode     Mode
		Policies []policy
	}{mode, policies}); err != nil {
		return "", fmt.Errorf("render policies: %w", err)
	}

	return out.String(), nil
}

// policyFor is one repository's policy data.
func policyFor(image Image) (policy, error) {
	// go-containerregistry's spelling of the repository, which is what the
	// admission webhook matches globs against: docker.io becomes
	// index.docker.io there and nowhere else.
	repository, err := name.NewRepository(image.Repository)
	if err != nil {
		return policy{}, err
	}

	p := policy{Name: PolicyName(image.Repository)}

	if errs := validation.IsDNS1123Subdomain(p.Name); len(errs) > 0 {
		return policy{}, fmt.Errorf("policy name %q: %s", p.Name, strings.Join(errs, "; "))
	}

	if image.Unsigned != nil {
		p.Pin = image.Unsigned
		p.Globs = []string{repository.Name() + "@" + image.Unsigned.Digest}

		return p, nil
	}

	if image.Signed == nil {
		return policy{}, errors.New("neither signed nor unsigned")
	}

	p.Signer = image.Signed
	// A digest reference once the webhook has resolved the tag, and a tag
	// reference when it could not — which the policy then refuses as not a
	// digest, rather than letting it fall through to no policy at all.
	p.Globs = []string{repository.Name() + "@*", repository.Name() + ":*"}

	if image.Signed.Key != "" {
		key, err := PublicKey(image.Signed.Key)
		if err != nil {
			return policy{}, err
		}

		p.KeyPEM = string(key)

		p.HashAlgorithm = image.Signed.HashAlgorithm
		if p.HashAlgorithm == "" {
			p.HashAlgorithm = DefaultHashAlgorithm
		}

		if !image.Signed.NoTransparencyLog {
			p.TransparencyLog = PublicRekor
		}
	}

	return p, nil
}

// PolicyName is the ClusterImagePolicy generated for a repository.
func PolicyName(repository string) string {
	return policyPrefix + strings.ReplaceAll(repository, "/", "-")
}

// quote writes a string as a JSON string, which YAML reads as a double-quoted
// scalar: a regular expression's backslashes and a glob's asterisk survive it.
func quote(s string) (string, error) {
	out, err := json.Marshal(s)

	return string(out), err
}

// indent pads every non-empty line of a block by the given number of spaces.
func indent(spaces int, text string) string {
	pad := strings.Repeat(" ", spaces)

	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = pad + line
		}
	}

	return strings.Join(lines, "\n")
}
