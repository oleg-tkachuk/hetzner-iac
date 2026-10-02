// Command image bakes the Talos snapshot the topology names into the
// hcloud project, and is idempotent.
//
// It replaces a shell block that had grown to forty-eight lines and five
// tools: `curl | jq` for the Image Factory, `hcloud image list | awk` for the
// idempotence check — both now the vendors' own Go clients — a `case` for the architecture, and a Taskfile `env:`
// stanza that resolved the token before the task's own preconditions could
// run — the last one being a trap this repository walked into: Task evaluates
// `env` first, so the precondition holding the remedy was unreachable.
//
// What stays external is the upload itself. hcloud-upload-image creates a
// server, writes the image to its disk and snapshots it; importing that as a
// library would pull in its dependency tree and tie this repository to its
// API, for a step that is one process invocation.
package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/siderolabs/image-factory/pkg/client"
	"github.com/siderolabs/image-factory/pkg/schematic"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/hcloudtoken"
)

// factoryURL is the Talos Image Factory. Images come from there rather than
// from GitHub releases: the schematic id is content-addressed, so posting the
// customisation is deterministic, and it is where system extensions go later —
// no hash to keep in sync by hand.
const factoryURL = "https://factory.talos.dev"

// factoryRegistry is the factory's container registry, where installer images
// live: the host of factoryURL.
const factoryRegistry = "factory.talos.dev"

// installerRepository is the factory's Hetzner Cloud installer, the platform
// the snapshot is baked for. Talos stopped publishing ghcr.io/siderolabs/installer
// with v1.14, so an upgrade to it can only come from here.
const installerRepository = "hcloud-installer"

// installerCommand is the subcommand that prints the installer image a node
// upgrades to, for the topology's version.
const installerCommand = "installer"

// factoryTimeout bounds the two Image Factory calls. Generous, because the
// factory builds on demand.
const factoryTimeout = 2 * time.Minute

// architectures maps the topology's spelling to the factory's. Talos says x86
// and arm; the factory says amd64 and arm64.
//
// The keys are internal/pkg/clusterspec's constants, not literals. They were
// literals, and the failure that allows is quiet in the wrong direction: the
// topology validator accepts whatever is in hetzner.Architectures, so an
// architecture added there would pass validation and then be refused here by
// a message naming the two this map happens to know.
// TestArchitectures_CoverEveryOneTheTopologyAccepts holds the two sets equal.
var architectures = map[string]string{
	clusterspec.ArchitectureX86: "amd64",
	clusterspec.ArchitectureARM: "arm64",
}

func main() {
	// No overall timeout: the upload creates a server, writes an image to its
	// disk and snapshots it, which takes as long as Hetzner takes. Ctrl-C is
	// what cancels it, and CommandContext is what makes that reach the child.
	if err := run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// osArgs is os.Args, indirected so the argument check can be tested without
// a topology file or a Pulumi backend.
var osArgs = os.Args

func run(ctx context.Context) error {
	if len(osArgs) == 3 && osArgs[1] == installerCommand {
		return printInstaller(ctx, osArgs[2])
	}

	if len(osArgs) != 3 {
		return fmt.Errorf("usage: image <topology.yaml> <stack> | image %s <topology.yaml>", installerCommand)
	}

	topologyPath, stack := osArgs[1], osArgs[2]

	topology, err := clusterspec.LoadTopology(topologyPath)
	if err != nil {
		return err
	}

	// From the parser the cluster itself is built from, not from a grep over
	// the file. The grep this replaced read three lines after `talos:` and
	// hoped the field was among them; it returned an empty version for one
	// topology, where a comment above the field had grown past the window.
	version := topology.Talos.Version
	arch := topology.Talos.Architecture

	factoryArch, err := factoryArchitecture(arch)
	if err != nil {
		return err
	}

	token, err := hcloudtoken.Token(ctx, stack)
	if err != nil {
		return err
	}

	selector := clusterspec.TalosImageSelector(version)

	present, err := snapshotExists(ctx, &hcloud.NewClient(hcloud.WithToken(token)).Image, selector, arch)
	if err != nil {
		return err
	}

	// An existing snapshot with these labels is what the Pulumi lookup
	// resolves, so re-baking would cost money and leave two candidates behind
	// for `mostRecent` to choose between.
	if present {
		fmt.Printf("snapshot already present for %s (%s) — nothing to do\n", selector, arch)

		return nil
	}

	factory, err := newFactory()
	if err != nil {
		return err
	}

	id, err := schematicID(ctx, factory)
	if err != nil {
		return err
	}

	url := imageURL(id, version, factoryArch)
	location := topology.Placement.Location

	fmt.Printf("baking %s (%s) in %s from %s\n", version, arch, location, url)

	if err := upload(ctx, token, url, arch, location, selector); err != nil {
		return err
	}

	fmt.Printf("snapshot ready — next: task cluster:apply stack=%s\n", stack)

	return nil
}

// printInstaller prints the installer image for the topology's Talos
// version, from the same schematic the snapshot is baked from.
func printInstaller(ctx context.Context, topologyPath string) error {
	topology, err := clusterspec.LoadTopology(topologyPath)
	if err != nil {
		return err
	}

	factory, err := newFactory()
	if err != nil {
		return err
	}

	id, err := schematicID(ctx, factory)
	if err != nil {
		return err
	}

	fmt.Println(installerImage(id, topology.Talos.Version))

	return nil
}

// installerImage is the factory's installer reference for a schematic and a
// Talos version.
func installerImage(schematic, version string) string {
	return fmt.Sprintf("%s/%s/%s:%s", factoryRegistry, installerRepository, schematic, version)
}

// imageURL is where the factory serves a built image. Separated from the call
// that needs it so the shape can be pinned without a network: a wrong path
// here fails inside hcloud-upload-image, minutes later, as a download error.
func imageURL(schematic, version, factoryArch string) string {
	return fmt.Sprintf("%s/image/%s/%s/hcloud-%s.raw.xz", factoryURL, schematic, version, factoryArch)
}

// factoryArchitecture maps the topology's spelling to the factory's.
func factoryArchitecture(arch string) (string, error) {
	factoryArch, known := architectures[arch]
	if !known {
		// The list from the map rather than from the sentence, so a third
		// architecture cannot be named in one and missing from the other.
		return "", fmt.Errorf("talos.architecture must be one of %s, got %q",
			strings.Join(slices.Sorted(maps.Keys(architectures)), ", "), arch)
	}

	return factoryArch, nil
}

// imageLister is the part of hcloud-go's image client this needs, so the check
// can be tested without the API.
type imageLister interface {
	AllWithOpts(ctx context.Context, opts hcloud.ImageListOpts) ([]*hcloud.Image, error)
}

// snapshotQuery is the list request that answers "is it already baked?".
//
// The architecture is the load-bearing part, and it was missing. The labels
// carry the Talos version but not the architecture, so the selector alone
// matched a snapshot of EITHER, and an Arm topology found the x86 one:
//
//	$ hcloud image list --type snapshot --selector os=talos,talos-version=v1.13.10
//	430516130   snapshot 2026-09-11T00:18:18Z   x86
//	$ … --architecture arm
//	[]
//
// which made `cluster:image:bake` print "snapshot already present — nothing
// to do" and exit 0, after which `cluster:apply` failed with "no available
// Talos snapshot matches selector … for architecture arm — run
// `task cluster:image:bake`". The remedy it named was the command that had
// just refused to run, so the two steps pointed at each other and no Arm
// image could ever be baked.
//
// Filtered by the API rather than over the returned list: architecture is a
// field Hetzner indexes and validates, one fewer place to spell the pair.
func snapshotQuery(selector, arch string) hcloud.ImageListOpts {
	return hcloud.ImageListOpts{
		ListOpts:     hcloud.ListOpts{LabelSelector: selector},
		Type:         []hcloud.ImageType{hcloud.ImageTypeSnapshot},
		Architecture: []hcloud.Architecture{hcloud.Architecture(arch)},
	}
}

// snapshotExists asks whether the selector already matches a snapshot of this
// architecture.
//
// Through hcloud-go, which this repository already uses, rather than the
// hcloud CLI and its JSON: the SDK moves with the API, and a decoded list of
// the CLI's output was a second reader of Hetzner's format kept here.
func snapshotExists(ctx context.Context, images imageLister, selector, arch string) (bool, error) {
	found, err := images.AllWithOpts(ctx, snapshotQuery(selector, arch))
	if err != nil {
		return false, fmt.Errorf("list snapshots: %w", err)
	}

	return len(found) > 0, nil
}

// schematicCreator is the part of the Image Factory client this needs, so the
// call can be tested without the factory.
type schematicCreator interface {
	SchematicCreate(ctx context.Context, sc schematic.Schematic) (string, *schematic.Schematic, error)
}

// newFactory is Sidero's own Image Factory client.
//
// It replaced a POST written here by hand, which demanded a 200 the factory
// never sends — it answers 201 Created, even for a repeat of an identical
// body — and so could not bake anything at all until the first bake for a
// second architecture found it. The client is the factory's own reading of
// its API, and moves with it.
func newFactory() (*client.Client, error) {
	factory, err := client.New(factoryURL)
	if err != nil {
		return nil, fmt.Errorf("image factory client: %w", err)
	}

	return factory, nil
}

// schematicID registers the customisation — none: the stock Hetzner image —
// and returns its content-addressed id.
func schematicID(ctx context.Context, factory schematicCreator) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, factoryTimeout)
	defer cancel()

	id, _, err := factory.SchematicCreate(ctx, schematic.Schematic{})
	if err != nil {
		return "", fmt.Errorf("image factory: %w", err)
	}

	if id == "" {
		return "", errors.New("image factory returned no schematic id")
	}

	return id, nil
}

// uploadArgs is the hcloud-upload-image invocation that bakes the snapshot.
//
// Separated from the call for the same reason imageURL is: nothing here fails
// here. A missing or misspelled flag fails inside a subprocess minutes later,
// after a server has already been created and paid for.
//
// --compression xz matches the factory's .raw.xz; the labels are what the
// Pulumi lookup then selects on, so they are the selector itself rather than a
// second spelling of it.
func uploadArgs(url, arch, location, selector string) []string {
	return []string{
		"upload",
		"--image-url", url,
		"--compression", "xz",
		"--architecture", arch,
		"--location", location,
		"--labels", selector,
	}
}

// upload runs hcloud-upload-image, streaming its output.
//
// --location is the topology's, not the tool's default of fsn1. It bakes by
// creating a real server, so the location has to be one that can hold the
// server type — and Arm is where that stops being academic: the cax line is
// offered in some locations and not others, so a bake that ignored the
// topology could fail in fsn1 for a cluster that lives in hel1. Baking where
// the cluster lives also means the temporary server shares its network zone.
func upload(ctx context.Context, token, url, arch, location, selector string) error {
	// #nosec G204 -- every argument is derived from the committed topology and
	// validated above; they are passed as a vector, not a shell string.
	cmd := exec.CommandContext(ctx, "hcloud-upload-image", uploadArgs(url, arch, location, selector)...)

	cmd.Env = append(os.Environ(), "HCLOUD_TOKEN="+token)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("hcloud-upload-image: %w", err)
	}

	return nil
}
