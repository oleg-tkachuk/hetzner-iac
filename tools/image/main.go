// Command image bakes the Talos snapshot the topology names into the
// hcloud project, and is idempotent.
//
// It replaces a shell block that had grown to forty-eight lines and five
// tools: `curl | jq` for the Image Factory, `hcloud image list | awk` for the
// idempotence check, a `case` for the architecture, and a Taskfile `env:`
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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/hetzner"
)

// factoryURL is the Talos Image Factory. Images come from there rather than
// from GitHub releases: the schematic id is content-addressed, so posting the
// customisation is deterministic, and it is where system extensions go later —
// no hash to keep in sync by hand.
const factoryURL = "https://factory.talos.dev"

// factoryTimeout bounds the two Image Factory calls. Generous, because the
// factory builds on demand.
const factoryTimeout = 2 * time.Minute

// architectures maps the topology's spelling to the factory's. Talos says x86
// and arm; the factory says amd64 and arm64.
//
// The keys are internal/pkg/hetzner's constants, not literals. They were
// literals, and the failure that allows is quiet in the wrong direction: the
// topology validator accepts whatever is in hetzner.Architectures, so an
// architecture added there would pass validation and then be refused here by
// a message naming the two this map happens to know.
// TestArchitectures_CoverEveryOneTheTopologyAccepts holds the two sets equal.
var architectures = map[string]string{
	hetzner.ArchitectureX86: "amd64",
	hetzner.ArchitectureARM: "arm64",
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
	if len(osArgs) != 3 {
		return fmt.Errorf("usage: image <topology.yaml> <stack>")
	}

	topologyPath, stack := osArgs[1], osArgs[2]

	topology, err := hetzner.LoadTopology(topologyPath)
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

	token, err := hetzner.Token(ctx, stack)
	if err != nil {
		return err
	}

	selector := hetzner.TalosImageSelector(version)

	present, err := snapshotExists(ctx, token, selector, arch)
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

	schematic, err := schematicID(ctx)
	if err != nil {
		return err
	}

	url := imageURL(schematic, version, factoryArch)
	location := topology.Placement.Location

	fmt.Printf("baking %s (%s) in %s from %s\n", version, arch, location, url)

	if err := upload(ctx, token, url, arch, location, selector); err != nil {
		return err
	}

	fmt.Printf("snapshot ready — next: task cluster:apply stack=%s\n", stack)

	return nil
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

// hcloudImage is the part of `hcloud image list -o json` this needs.
type hcloudImage struct {
	ID          int64  `json:"id"`
	Description string `json:"description"`
}

// listArgs is the hcloud invocation that answers "is it already baked?".
//
// --architecture is the load-bearing part, and it was missing. The labels
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
// Filtered by the API rather than over the decoded list: architecture is a
// field Hetzner indexes, and a flag it validates against x86|arm is one fewer
// place to spell the pair.
func listArgs(selector, arch string) []string {
	return []string{
		"image", "list",
		"--type", "snapshot",
		"--selector", selector,
		"--architecture", arch,
		"-o", "json",
	}
}

// snapshotExists asks whether the selector already matches a snapshot of this
// architecture.
//
// `-o json` and encoding/json rather than `-o noheader` piped into awk: the
// pipeline could not tell an empty list from a failed call, because grep and
// awk both answer "no rows" with exit 1.
func snapshotExists(ctx context.Context, token, selector, arch string) (bool, error) {
	// #nosec G204 -- the arguments are built here from the committed topology,
	// and passed as a vector rather than a shell string.
	cmd := exec.CommandContext(ctx, "hcloud", listArgs(selector, arch)...)

	cmd.Env = append(os.Environ(), "HCLOUD_TOKEN="+token)

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("hcloud image list: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	return snapshotMatched(out)
}

// snapshotMatched answers the question the awk pipeline answered, separated
// from the call so it can be tested without hcloud.
//
// An unparseable list is an error rather than "no snapshot": reported as
// absent, a broken response would bake an image that already exists and leave
// two candidates behind for the Pulumi lookup's mostRecent to choose between.
func snapshotMatched(raw []byte) (bool, error) {
	var images []hcloudImage
	if err := json.Unmarshal(raw, &images); err != nil {
		return false, fmt.Errorf("hcloud image list returned no usable json: %w", err)
	}

	return len(images) > 0, nil
}

// createdSchematic accepts any 2xx from POST /schematics.
//
// It used to demand exactly 200, and the factory answers 201 Created — which
// is correct for a POST that creates a resource, and is what it returns even
// for a repeat of an identical body, the id being content-addressed. So
// `cluster:image:bake` could not bake anything at all:
//
//	error: image factory returned 201 Created
//
// Hidden for as long as the project had a snapshot, because the check for one
// returns before the factory is ever called. The first thing to reach this
// line in weeks was the first bake for a second architecture.
//
// A range rather than 200 or 201 spelled out: the status is the transport's
// verdict on whether a schematic came back, and decodeSchematic is what
// decides whether the body actually holds one.
func createdSchematic(status int) bool {
	return status >= http.StatusOK && status < http.StatusMultipleChoices
}

// schematicID posts the customisation and returns the content-addressed id.
func schematicID(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, factoryTimeout)
	defer cancel()

	body := bytes.NewReader([]byte(`{"customization":{}}`))

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, factoryURL+"/schematics", body)
	if err != nil {
		return "", err
	}

	request.Header.Set("Content-Type", "application/json")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("image factory: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	if !createdSchematic(response.StatusCode) {
		return "", fmt.Errorf("image factory returned %s", response.Status)
	}

	return decodeSchematic(response.Body)
}

// decodeSchematic reads the schematic id out of the factory's response.
//
// An empty id is refused rather than passed on: it builds a URL the factory
// serves nothing at, and that surfaces minutes later inside
// hcloud-upload-image as a download error.
func decodeSchematic(body io.Reader) (string, error) {
	var decoded struct {
		ID string `json:"id"`
	}

	if err := json.NewDecoder(body).Decode(&decoded); err != nil {
		return "", fmt.Errorf("image factory response: %w", err)
	}

	if decoded.ID == "" {
		return "", errors.New("image factory returned no schematic id")
	}

	return decoded.ID, nil
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
