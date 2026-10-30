package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"go.yaml.in/yaml/v3"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/charts"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/imagepolicy"
)

// inventoryPath is the image inventory on disk, from the repository root:
// what repin rewrites. The build embeds the same file, which is what it reads.
var inventoryPath = filepath.Join(imagepolicy.Dir, imagepolicy.File)

// repin moves every unsigned pin to the tag its chart now renders by default,
// and to the index digest that tag resolves to.
//
// It is what a chart bump needs and what checkPins asks for by hand: Renovate
// runs it after raising a chart's version, so the pull request carries the new
// digest rather than failing `charts render` until somebody runs crane. It
// writes only the tag and digest lines, so the file's comments and layout are
// left as they were.
func repin(ctx context.Context) error {
	inventory, err := imagepolicy.Load()
	if err != nil {
		return err
	}

	return repinFile(ctx, os.Stdout, inventory, inventoryPath, renderRaw, resolveDigest)
}

// resolveFunc is the digest a tag points to now: resolveDigest, or a fixture
// in a test.
type resolveFunc func(ctx context.Context, repository, tag string) (string, error)

// repinFile is repin with its inputs passed in, so a test can rewrite a
// temporary inventory against fixtures.
func repinFile(
	ctx context.Context, w io.Writer, inventory *imagepolicy.Inventory, path string,
	render renderFunc, resolve resolveFunc,
) error {
	tags, err := renderedPinTags(ctx, render, inventory)
	if err != nil {
		return err
	}

	// #nosec G304 -- inventoryPath in production, a temporary file in tests; never user input.
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	raw, moved, err := movePins(ctx, w, inventory, tags, raw, resolve)
	if err != nil {
		return err
	}

	if moved == 0 {
		fmt.Fprintln(w, "every pin already follows its chart")

		return nil
	}

	// The result must still be an inventory a policy can be built from.
	if _, err := imagepolicy.Parse(raw); err != nil {
		return fmt.Errorf("the rewritten %s does not parse: %w", imagepolicy.File, err)
	}

	return os.WriteFile(path, raw, inventoryMode) // #nosec G306,G703 -- a committed text file at a constant path
}

// movePins rewrites, in the inventory's text, each pin whose rendered tag or
// resolved digest moved, and returns the text and how many moved.
func movePins(
	ctx context.Context, w io.Writer, inventory *imagepolicy.Inventory, tags map[string]string,
	raw []byte, resolve resolveFunc,
) ([]byte, int, error) {
	moved := 0

	for _, repository := range inventory.Repositories() {
		tag, rendered := tags[repository]
		if !rendered {
			continue
		}

		pin, err := inventory.Pinned(repository)
		if err != nil {
			return nil, 0, err
		}

		digest, err := resolve(ctx, repository, tag)
		if err != nil {
			return nil, 0, err
		}

		if tag == pin.Tag && digest == pin.Digest {
			continue
		}

		raw, err = setPin(raw, repository, tag, digest)
		if err != nil {
			return nil, 0, err
		}

		fmt.Fprintf(w, "pinned  %s  %s → %s@%s\n", repository, pin.Reference(), tag, digest)

		moved++
	}

	return raw, moved, nil
}

// renderedPinTags is the tag each unsigned repository's chart renders by
// default, by repository.
func renderedPinTags(ctx context.Context, render renderFunc, inventory *imagepolicy.Inventory) (map[string]string, error) {
	tags := map[string]string{}

	for _, key := range charts.Keys() {
		chart, chartErr := charts.Get(key)
		if chartErr != nil {
			return nil, chartErr
		}

		images, renderErr := defaultImages(ctx, render, key, chart)
		if renderErr != nil {
			return nil, fmt.Errorf("%s: %w", key, renderErr)
		}

		if err := addUnsignedTags(tags, images, inventory); err != nil {
			return nil, err
		}
	}

	return tags, nil
}

// addUnsignedTags records the tag of each image whose repository is pinned
// rather than signed.
func addUnsignedTags(tags map[string]string, images []string, inventory *imagepolicy.Inventory) error {
	for _, image := range images {
		entry, found, err := inventory.Lookup(image)
		if err != nil {
			return err
		}

		if found && entry.Unsigned != nil {
			tags[entry.Repository] = tagOf(image)
		}
	}

	return nil
}

// inventoryMode is the file's mode when rewritten: a committed text file.
const inventoryMode = 0o644

// resolveDigest is the digest a tag points to now: the index digest for a
// multi-architecture image, which is what `crane digest` prints and what the
// runtime resolves a tag through.
func resolveDigest(ctx context.Context, repository, tag string) (string, error) {
	ref, err := name.NewTag(repository + ":" + tag)
	if err != nil {
		return "", err
	}

	descriptor, err := remote.Head(ref, remote.WithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", ref, err)
	}

	return descriptor.Digest.String(), nil
}

// setPin rewrites one repository's pin in the inventory's text.
//
// The YAML is parsed only to find where the two values sit; the bytes on
// those lines are then replaced in place. Re-encoding the document instead
// would reflow it — indentation, quoting, the long patterns — and turn a
// two-line change into a review of the whole file.
func setPin(raw []byte, repository, tag, digest string) ([]byte, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, err
	}

	tagNode, digestNode, err := pinNodes(&document, repository)
	if err != nil {
		return nil, err
	}

	lines := bytes.Split(raw, []byte("\n"))

	for _, edit := range []struct {
		node  *yaml.Node
		value string
	}{{tagNode, tag}, {digestNode, digest}} {
		line := lines[edit.node.Line-1]
		start := edit.node.Column - 1

		if !bytes.HasPrefix(line[start:], []byte(edit.node.Value)) {
			return nil, fmt.Errorf("%s: %q is not where the parser put it on line %d",
				repository, edit.node.Value, edit.node.Line)
		}

		replaced := append([]byte{}, line[:start]...)
		replaced = append(replaced, edit.value...)
		replaced = append(replaced, line[start+len(edit.node.Value):]...)
		lines[edit.node.Line-1] = replaced
	}

	return bytes.Join(lines, []byte("\n")), nil
}

// The inventory's keys setPin walks, as images.yaml spells them.
const (
	keyImages     = "images"
	keyRepository = "repository"
	keyUnsigned   = "unsigned"
	keyTag        = "tag"
	keyDigest     = "digest"
)

var errNoPin = errors.New("no unsigned pin")

// pinNodes finds a repository's tag and digest values in the parsed document.
func pinNodes(document *yaml.Node, repository string) (*yaml.Node, *yaml.Node, error) {
	if len(document.Content) == 0 {
		return nil, nil, errors.New("empty inventory")
	}

	images := mappingValue(document.Content[0], keyImages)
	if images == nil || images.Kind != yaml.SequenceNode {
		return nil, nil, fmt.Errorf("no %s list", keyImages)
	}

	for _, entry := range images.Content {
		if value := mappingValue(entry, keyRepository); value == nil || value.Value != repository {
			continue
		}

		unsigned := mappingValue(entry, keyUnsigned)
		tag, digest := mappingValue(unsigned, keyTag), mappingValue(unsigned, keyDigest)

		if tag == nil || digest == nil {
			return nil, nil, fmt.Errorf("%s: %w", repository, errNoPin)
		}

		return tag, digest, nil
	}

	return nil, nil, fmt.Errorf("%s has no entry in %s", repository, imagepolicy.File)
}

// mappingValue is a key's value in a mapping node, or nil.
func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}

	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}

	return nil
}
