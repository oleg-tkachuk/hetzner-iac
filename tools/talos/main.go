// Command talos checks the machine-config patches against Talos itself.
//
// The unit tests in pkg/hetzner prove the patches contain what was intended.
// They cannot prove Talos accepts them — and Talos is strict in ways that are
// not guessable. This generates a baseline configuration for the pinned
// version, applies the patches this repository produces, and runs
// `talosctl validate`.
//
// It is not hypothetical. The first run found that machine.network.hostname is
// rejected outright, because a HostnameConfig document is always present and
// setting the name in both places is a conflict. That would have failed at
// apply — after the servers existed and were being paid for.
//
// The talosctl in PATH must match the version the topology pins. Talos moves
// configuration between documents across minor versions, so validating a 1.13
// config with a 1.14 binary reports conflicts that do not exist, and the other
// way round misses real ones.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/hetzner"
)

const clusterEndpoint = "https://10.0.1.2:6443"

func main() {
	dir := "infra/cluster"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}

	if err := run(dir); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(dir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if _, err := exec.LookPath("talosctl"); err != nil {
		return fmt.Errorf("talosctl is not installed: %w", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read %s: %w", dir, err)
	}

	checked := 0

	for _, entry := range entries {
		if entry.IsDir() || !isTopologyFile(entry.Name()) {
			continue
		}

		path := filepath.Join(dir, entry.Name())

		topology, err := hetzner.LoadTopology(path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}

		if err := checkVersion(ctx, topology.Talos.Version); err != nil {
			return err
		}

		if err := validateTopology(ctx, path, topology); err != nil {
			return err
		}

		checked++
	}

	if checked == 0 {
		return fmt.Errorf("no cluster.<stack>.yaml files under %s", dir)
	}

	return nil
}

// checkVersion refuses to run when the local talosctl does not match the
// pinned Talos minor version.
//
// This is the check that matters most, because without it the tool lies
// confidently: a 1.14 binary validating a 1.13 configuration reports document
// conflicts that will not occur on the cluster being built.
func checkVersion(ctx context.Context, pinned string) error {
	output, err := exec.CommandContext(ctx, "talosctl", "version", "--client", "--short").Output()
	if err != nil {
		// --short is not in every release; fall back to the long form.
		output, err = exec.CommandContext(ctx, "talosctl", "version", "--client").Output()
		if err != nil {
			return fmt.Errorf("talosctl version: %w", err)
		}
	}

	local := extractTag(string(output))
	if local == "" {
		return fmt.Errorf("could not read the talosctl version from %q", strings.TrimSpace(string(output)))
	}

	if minor(local) != minor(pinned) {
		// The explanation is printed rather than wrapped into the error:
		// it is guidance for a person, and an error value should stay a
		// single line so it reads correctly wherever it is logged.
		fmt.Fprintf(os.Stderr,
			"Talos moves configuration between documents across minor versions, so a\n"+
				"mismatched binary reports conflicts that will not happen — or misses real\n"+
				"ones. Install talosctl %s and run this again.\n\n", pinned)

		return fmt.Errorf("talosctl is %s but the topology pins Talos %s", local, pinned)
	}

	return nil
}

// validateTopology renders the patches for one topology and validates the
// control-plane and worker configurations they produce.
func validateTopology(ctx context.Context, path string, topology *hetzner.Topology) error {
	clusterPatch, err := hetzner.BuildClusterPatch(hetzner.ClusterPatchArgs{
		PodCIDR:                        topology.Network.PodCIDR,
		ServiceCIDR:                    topology.Network.ServiceCIDR,
		NodeSubnet:                     topology.Network.NodeSubnet,
		AllowSchedulingOnControlPlanes: len(topology.WorkerPools) == 0,
	})
	if err != nil {
		return err
	}

	nodePatch, err := hetzner.BuildNodePatch(hetzner.NodePatchArgs{
		Hostname: hetzner.NodeName(topology.Metadata.Name, hetzner.RoleControlPlane, 0),
		CertSANs: []string{"203.0.113.10", "10.0.1.2"},
	})
	if err != nil {
		return err
	}

	workDir, err := os.MkdirTemp("", "talos-validate-")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}

	defer func() { _ = os.RemoveAll(workDir) }()

	if err := generate(ctx, workDir, topology); err != nil {
		return err
	}

	for _, machine := range []string{"controlplane", "worker"} {
		patches := []string{clusterPatch}
		// A worker takes the cluster patch only: the node patch carries a
		// control-plane hostname and certificate SANs.
		if machine == "controlplane" {
			patches = append(patches, nodePatch)
		}

		if err := validateMachine(ctx, workDir, machine, patches); err != nil {
			return fmt.Errorf("%s (%s): %w", path, machine, err)
		}

		fmt.Printf("ok    %-34s %s\n", filepath.Base(path), machine)
	}

	return nil
}

func generate(ctx context.Context, workDir string, topology *hetzner.Topology) error {
	args := []string{
		"gen", "config", topology.Metadata.Name, clusterEndpoint,
		"--output-dir", workDir,
		"--with-docs=false", "--with-examples=false",
		"--talos-version", topology.Talos.Version,
		"--force",
	}

	// #nosec G204,G702 -- the only non-literal arguments are the cluster name
	// and the Talos version, both of which pkg/hetzner validated before this
	// ran: the name against DNS-1123, the version against vX.Y.Z. Nothing
	// reaches a shell.
	if output, err := exec.CommandContext(ctx, "talosctl", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("talosctl gen config: %s", strings.TrimSpace(string(output)))
	}

	return nil
}

func validateMachine(ctx context.Context, workDir, machine string, patches []string) error {
	base := filepath.Join(workDir, machine+".yaml")
	patched := filepath.Join(workDir, machine+"-patched.yaml")

	args := []string{"machineconfig", "patch", base}

	for i, patch := range patches {
		file := filepath.Join(workDir, fmt.Sprintf("%s-patch-%d.yaml", machine, i))
		if err := os.WriteFile(file, []byte(patch), 0o600); err != nil {
			return fmt.Errorf("write patch: %w", err)
		}

		args = append(args, "--patch", "@"+file)
	}

	args = append(args, "-o", patched)

	// #nosec G204,G702 -- every element of argv is either a literal or a path
	// this program created under its own temp directory. Nothing reaches a
	// shell, and the topology values that gosec traces here were validated
	// before they got this far.
	if output, err := exec.CommandContext(ctx, "talosctl", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("applying the patches failed: %s", strings.TrimSpace(string(output)))
	}

	// cloud mode: these nodes are cloud instances, and the mode changes which
	// rules apply — a container-mode validation would pass configurations a
	// real node rejects.
	// #nosec G204 -- fixed argv.
	cmd := exec.CommandContext(ctx, "talosctl", "validate", "--config", patched, "--mode", "cloud")

	if output, err := cmd.CombinedOutput(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("talos rejected the configuration:\n%s", indent(string(output)))
		}

		return err
	}

	return nil
}

// isTopologyFile mirrors the naming infra/cluster/main.go derives from the
// stack name.
func isTopologyFile(name string) bool {
	if !strings.HasSuffix(name, ".yaml") {
		return false
	}

	stack, found := strings.CutPrefix(strings.TrimSuffix(name, ".yaml"), "cluster.")

	return found && stack != "" && !strings.Contains(stack, ".")
}

// extractTag pulls a vX.Y.Z tag out of talosctl's version output.
//
// The format differs between invocations — `--short` prints "Talos v1.13.10",
// the long form prints an indented "Tag: v1.13.10" — so this scans for the
// version token rather than matching a layout.
func extractTag(output string) string {
	return versionToken.FindString(output)
}

// versionToken matches a vX.Y.Z release tag.
var versionToken = regexp.MustCompile(`v\d+\.\d+\.\d+`)

// minor reduces vX.Y.Z to vX.Y. Talos's configuration model changes between
// minors, not patches.
func minor(version string) string {
	parts := strings.SplitN(strings.TrimPrefix(version, "v"), ".", 3)
	if len(parts) < 2 {
		return version
	}

	return "v" + parts[0] + "." + parts[1]
}

func indent(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, line := range lines {
		lines[i] = "      " + line
	}

	return strings.Join(lines, "\n")
}
