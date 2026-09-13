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
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/hetzner"
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
var architectures = map[string]string{
	"x86": "amd64",
	"arm": "arm64",
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

	selector := "os=talos,talos-version=" + version

	present, err := snapshotExists(ctx, token, selector)
	if err != nil {
		return err
	}

	// An existing snapshot with these labels is what the Pulumi lookup
	// resolves, so re-baking would cost money and leave two candidates behind
	// for `mostRecent` to choose between.
	if present {
		fmt.Printf("snapshot already present for %s — nothing to do\n", selector)

		return nil
	}

	schematic, err := schematicID(ctx)
	if err != nil {
		return err
	}

	url := imageURL(schematic, version, factoryArch)
	fmt.Printf("baking %s (%s) from %s\n", version, arch, url)

	if err := upload(ctx, token, url, arch, selector); err != nil {
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
		return "", fmt.Errorf("talos.architecture must be x86 or arm, got %q", arch)
	}

	return factoryArch, nil
}

// hcloudImage is the part of `hcloud image list -o json` this needs.
type hcloudImage struct {
	ID          int64  `json:"id"`
	Description string `json:"description"`
}

// snapshotExists asks whether the selector already matches a snapshot.
//
// `-o json` and encoding/json rather than `-o noheader` piped into awk: the
// pipeline could not tell an empty list from a failed call, because grep and
// awk both answer "no rows" with exit 1.
func snapshotExists(ctx context.Context, token, selector string) (bool, error) {
	// #nosec G204 -- the selector is built here from the topology's Talos
	// version, and the arguments are a vector rather than a shell string.
	cmd := exec.CommandContext(ctx, "hcloud", "image", "list",
		"--type", "snapshot", "--selector", selector, "-o", "json")

	cmd.Env = append(os.Environ(), "HCLOUD_TOKEN="+token)

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("hcloud image list: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	var images []hcloudImage
	if err := json.Unmarshal(out, &images); err != nil {
		return false, fmt.Errorf("hcloud image list returned no usable json: %w", err)
	}

	return len(images) > 0, nil
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

	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("image factory returned %s", response.Status)
	}

	var decoded struct {
		ID string `json:"id"`
	}

	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return "", fmt.Errorf("image factory response: %w", err)
	}

	if decoded.ID == "" {
		return "", errors.New("image factory returned no schematic id")
	}

	return decoded.ID, nil
}

// upload runs hcloud-upload-image, streaming its output.
func upload(ctx context.Context, token, url, arch, selector string) error {
	// #nosec G204 -- url and arch are derived from the committed topology and
	// validated above; the arguments are a vector, not a shell string.
	cmd := exec.CommandContext(ctx, "hcloud-upload-image", "upload",
		"--image-url", url,
		"--compression", "xz",
		"--architecture", arch,
		"--labels", selector)

	cmd.Env = append(os.Environ(), "HCLOUD_TOKEN="+token)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("hcloud-upload-image: %w", err)
	}

	return nil
}
