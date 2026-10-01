// Package imagepolicy is the inventory of every image the platform installs,
// by repository: who signs it, or why nothing does. It is the source the
// admission policies are generated from, and tools/charts holds every rendered
// image to having an entry here.
package imagepolicy

import (
	"embed"
	"encoding/pem"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"

	"github.com/distribution/reference"
	"github.com/opencontainers/go-digest"
	"sigs.k8s.io/yaml"
)

// File is the inventory, beside this package.
const File = "images.yaml"

//go:embed images.yaml
var raw []byte

// Signer is who signs a repository's images: either Sigstore keyless — the
// OIDC issuer and the certificate subject, exact or as a pattern — or a
// publisher's own key, committed under keys/.
type Signer struct {
	Issuer        string `json:"issuer,omitempty"`
	Subject       string `json:"subject,omitempty"`
	SubjectRegExp string `json:"subjectRegExp,omitempty"`

	// Key is a public key file under keys/, for a publisher that signs with
	// a key it holds rather than keylessly.
	Key string `json:"key,omitempty"`
	// HashAlgorithm is the digest the key signed over, when not sha256.
	HashAlgorithm string `json:"hashAlgorithm,omitempty"`
	// NoTransparencyLog is set for a publisher that does not record its
	// signatures in Rekor, so a policy must not demand an entry there.
	NoTransparencyLog bool `json:"noTransparencyLog,omitempty"`
}

//go:embed keys/*.pem
var keys embed.FS

// KeysDir is where the publishers' public keys are committed.
const KeysDir = "keys"

// PublicKey is the PEM of a signer's key file.
func PublicKey(name string) ([]byte, error) {
	data, err := keys.ReadFile(path.Join(KeysDir, name))
	if err != nil {
		return nil, fmt.Errorf("public key %s: %w", name, err)
	}

	if block, _ := pem.Decode(data); block == nil {
		return nil, fmt.Errorf("public key %s is not PEM", name)
	}

	return data, nil
}

// Pin is an unsigned repository's one admitted image: the tag its chart
// renders by default, and the index digest that tag resolved to when pinned.
//
// The tag is recorded so a chart upgrade cannot leave the digest behind. A
// chart bump moves its default tag; tools/charts compares that tag with this
// one and fails until the digest is moved with it, rather than keep running
// the old image with every check green.
type Pin struct {
	// Reason is why no signature can be required: the publisher signs nothing.
	Reason string `json:"reason"`
	Tag    string `json:"tag"`
	Digest string `json:"digest"`
}

// Image is one repository and how its provenance is established.
type Image struct {
	Repository string  `json:"repository"`
	Signed     *Signer `json:"signed,omitempty"`
	// Unsigned pins a repository that publishes no signature by digest.
	Unsigned *Pin `json:"unsigned,omitempty"`
}

// Inventory is the parsed file.
type Inventory struct {
	Images []Image `json:"images"`
}

var errInvalid = errors.New("invalid image inventory")

// Load parses and validates the embedded inventory.
func Load() (*Inventory, error) {
	return Parse(raw)
}

// Parse reads an inventory and refuses one a policy could not be built from.
func Parse(data []byte) (*Inventory, error) {
	var inventory Inventory
	if err := yaml.UnmarshalStrict(data, &inventory); err != nil {
		return nil, fmt.Errorf("%w: %w", errInvalid, err)
	}

	seen := map[string]bool{}

	for _, image := range inventory.Images {
		normal, err := Repository(image.Repository)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errInvalid, err)
		}

		if normal != image.Repository {
			return nil, fmt.Errorf("%w: %q is written %q in its normalised form", errInvalid, image.Repository, normal)
		}

		if seen[normal] {
			return nil, fmt.Errorf("%w: %s is listed twice", errInvalid, normal)
		}

		seen[normal] = true

		if err := image.validate(); err != nil {
			return nil, fmt.Errorf("%w: %s: %w", errInvalid, image.Repository, err)
		}
	}

	return &inventory, nil
}

func (i Image) validate() error {
	switch {
	case i.Signed == nil && i.Unsigned == nil:
		return errors.New("neither signed nor unsigned")
	case i.Signed != nil && i.Unsigned != nil:
		return errors.New("both signed and unsigned")
	case i.Signed == nil:
		return i.Unsigned.validate()
	case i.Signed.Key != "":
		if i.Signed.Issuer != "" || i.Signed.Subject != "" || i.Signed.SubjectRegExp != "" {
			return errors.New("signed by a key and keylessly at once")
		}

		_, err := PublicKey(i.Signed.Key)

		return err
	case i.Signed.Issuer == "":
		return errors.New("signed keylessly with no issuer")
	case (i.Signed.Subject == "") == (i.Signed.SubjectRegExp == ""):
		return errors.New("signed keylessly needs exactly one of subject and subjectRegExp")
	case i.Signed.SubjectRegExp != "":
		if _, err := regexp.Compile(i.Signed.SubjectRegExp); err != nil {
			return fmt.Errorf("subjectRegExp: %w", err)
		}
	}

	return nil
}

func (p Pin) validate() error {
	switch {
	case p.Reason == "":
		return errors.New("unsigned with no reason")
	case p.Tag == "" || reference.TagRegexp.FindString(p.Tag) != p.Tag:
		return fmt.Errorf("unsigned pin tag %q is not a tag", p.Tag)
	}

	if _, err := digest.Parse(p.Digest); err != nil {
		return fmt.Errorf("unsigned pin digest %q: %w", p.Digest, err)
	}

	return nil
}

// Reference is the pin as a chart's tag value takes it: `tag@digest`, where
// the digest is what the runtime pulls and the tag is what the chart's own
// version checks read.
func (p Pin) Reference() string {
	return p.Tag + "@" + p.Digest
}

// Pinned is an unsigned repository's pin.
func (inv *Inventory) Pinned(repository string) (Pin, error) {
	for _, entry := range inv.Images {
		if entry.Repository != repository {
			continue
		}

		if entry.Unsigned == nil {
			return Pin{}, fmt.Errorf("%s is signed, not pinned by digest", repository)
		}

		return *entry.Unsigned, nil
	}

	return Pin{}, fmt.Errorf("%s has no entry in %s", repository, File)
}

// Repository is an image reference's repository in its fully qualified form:
// docker.io/library/traefik for `traefik:v3`, tag and digest dropped.
func Repository(image string) (string, error) {
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return "", fmt.Errorf("parse image %q: %w", image, err)
	}

	return named.Name(), nil
}

// Lookup is the entry for an image reference, if the inventory has one.
func (inv *Inventory) Lookup(image string) (Image, bool, error) {
	repository, err := Repository(image)
	if err != nil {
		return Image{}, false, err
	}

	for _, entry := range inv.Images {
		if entry.Repository == repository {
			return entry, true, nil
		}
	}

	return Image{}, false, nil
}

// Repositories is every repository in the inventory, sorted.
func (inv *Inventory) Repositories() []string {
	out := make([]string, 0, len(inv.Images))
	for _, entry := range inv.Images {
		out = append(out, entry.Repository)
	}

	sort.Strings(out)

	return out
}
