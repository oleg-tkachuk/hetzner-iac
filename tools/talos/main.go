// Command talos checks the machine-config patches against Talos itself.
//
// The unit tests in internal/pkg/clusterspec prove the patches contain what was intended.
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
	"runtime"
	"strings"
	"time"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
)

const clusterEndpoint = "https://10.0.1.2:6443"

func main() {
	dir := "infra/cluster"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}

	if err := run(dir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// LocalTalosctl is where `task cluster:talosctl:install` puts the binary the
// topology pins.
//
// Preferred over PATH, and that is the whole point of it: Homebrew carries one
// talosctl, the newest, and this check needs the minor the topology names. An
// operator should not have to choose between the two on their PATH.
const LocalTalosctl = "bin/talosctl"

// talosctlPath is the binary this check runs.
//
// The repository's own copy first, then PATH. Returned as a path rather than
// resolved once into a global, so a test can see which one was chosen.
func talosctlPath() (string, error) {
	if info, err := os.Stat(LocalTalosctl); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
		absolute, absErr := filepath.Abs(LocalTalosctl)
		if absErr != nil {
			return "", fmt.Errorf("%s: %w", LocalTalosctl, absErr)
		}

		return absolute, nil
	}

	found, err := exec.LookPath("talosctl")
	if err != nil {
		return "", fmt.Errorf("talosctl is not installed, and %s does not exist either: "+
			"`task cluster:talosctl:install` writes the pinned one there: %w",
			LocalTalosctl, err)
	}

	return found, nil
}

func run(dir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	talosctl, err := talosctlPath()
	if err != nil {
		return err
	}

	paths, err := topologyFiles(dir)
	if err != nil {
		return err
	}

	for _, path := range paths {
		topology, err := clusterspec.LoadTopology(path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}

		if err := checkVersion(ctx, talosctl, topology.Talos.Version); err != nil {
			return err
		}

		if err := validateTopology(ctx, talosctl, path, topology); err != nil {
			return err
		}
	}

	return nil
}

// topologyFiles lists the stack topologies in dir, and refuses an empty
// result.
//
// Separated from run so the two answers an operator actually meets — the
// wrong directory, and a directory with no stacks in it — are testable
// without talosctl. Reporting success for zero files checked is the failure
// worth ruling out: it is what a validation step run from the repository root
// would have done.
func topologyFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}

	var paths []string

	for _, entry := range entries {
		if entry.IsDir() || !isTopologyFile(entry.Name()) {
			continue
		}

		paths = append(paths, filepath.Join(dir, entry.Name()))
	}

	if len(paths) == 0 {
		return nil, fmt.Errorf("no cluster.<stack>.yaml files under %s", dir)
	}

	return paths, nil
}

// checkVersion refuses to run when the local talosctl does not match the
// pinned Talos minor version.
//
// This is the check that matters most, because without it the tool lies
// confidently: a 1.14 binary validating a 1.13 configuration reports document
// conflicts that will not occur on the cluster being built.
func checkVersion(ctx context.Context, talosctl, pinned string) error {
	output, err := exec.CommandContext(ctx, talosctl, "version", "--client", "--short").Output()
	if err != nil {
		// --short is not in every release; fall back to the long form.
		output, err = exec.CommandContext(ctx, talosctl, "version", "--client").Output()
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
		//
		// The task rather than a curl, because the version and the platform
		// are both known to it and a pasted curl is wrong the moment the pin
		// moves. It writes bin/talosctl, which talosctlPath prefers over
		// PATH — so Homebrew's newest can stay where it is.
		fmt.Fprintf(os.Stderr,
			"Talos moves configuration between documents across minor versions, so a\n"+
				"mismatched binary reports conflicts that will not happen — or misses real\n"+
				"ones. Get the matching one:\n\n"+
				"  task cluster:talosctl:install\n\n"+
				"That writes %s for %s/%s and this check prefers it, so `brew install\n"+
				"talosctl` can keep the newest on PATH for everything else. The pin moves\n"+
				"when pulumi-talos ships newer machinery — see talos.version in the\n"+
				"topology.\n\n",
			LocalTalosctl, runtime.GOOS, runtime.GOARCH)

		return fmt.Errorf("talosctl is %s but the topology pins Talos %s", local, pinned)
	}

	return nil
}

// validateTopology renders the patches for one topology and validates the
// control-plane and worker configurations they produce.
func validateTopology(ctx context.Context, talosctl, path string, topology *clusterspec.Topology) error {
	clusterPatch, err := clusterspec.BuildClusterPatch(clusterspec.ClusterPatchArgs{
		PodCIDR:                        topology.Network.PodCIDR,
		ServiceCIDR:                    topology.Network.ServiceCIDR,
		NodeSubnet:                     topology.Network.NodeSubnet,
		IPRange:                        topology.Network.IPRange,
		AllowSchedulingOnControlPlanes: len(topology.WorkerPools) == 0,
	})
	if err != nil {
		return err
	}

	nodePatch, err := clusterspec.BuildNodePatch(clusterspec.NodePatchArgs{
		Hostname: clusterspec.NodeName(topology.Metadata.Name, clusterspec.RoleControlPlane, 0),
		CertSANs: []string{"203.0.113.10", "10.0.1.2"},
	})
	if err != nil {
		return err
	}

	workDir, err := os.MkdirTemp("", "talos-validate-")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}

	// A scratch directory this function made. Leaving it behind on a failed
	// removal costs a directory in /tmp, not correctness.
	defer func() { _ = os.RemoveAll(workDir) }()

	if err := generate(ctx, talosctl, workDir, topology); err != nil {
		return err
	}

	for _, machine := range []string{"controlplane", "worker"} {
		patches := []string{clusterPatch}
		// A worker takes the cluster patch only: the node patch carries a
		// control-plane hostname and certificate SANs.
		if machine == "controlplane" {
			patches = append(patches, nodePatch)
		}

		if err := validateMachine(ctx, talosctl, workDir, machine, patches); err != nil {
			return fmt.Errorf("%s (%s): %w", path, machine, err)
		}

		fmt.Printf("ok    %-34s %s\n", filepath.Base(path), machine)
	}

	return nil
}

func generate(ctx context.Context, talosctl, workDir string, topology *clusterspec.Topology) error {
	args := []string{
		"gen", "config", topology.Metadata.Name, clusterEndpoint,
		"--output-dir", workDir,
		"--with-docs=false", "--with-examples=false",
		"--talos-version", topology.Talos.Version,
		"--force",
	}

	// #nosec G204,G702 -- the only non-literal arguments are the cluster name
	// and the Talos version, both of which internal/pkg/clusterspec validated before this
	// ran: the name against DNS-1123, the version against vX.Y.Z. Nothing
	// reaches a shell.
	if output, err := exec.CommandContext(ctx, talosctl, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("talosctl gen config: %s", strings.TrimSpace(string(output)))
	}

	return nil
}

func validateMachine(ctx context.Context, talosctl, workDir, machine string, patches []string) error {
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
	if output, err := exec.CommandContext(ctx, talosctl, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("applying the patches failed: %s", strings.TrimSpace(string(output)))
	}

	// cloud mode: these nodes are cloud instances, and the mode changes which
	// rules apply — a container-mode validation would pass configurations a
	// real node rejects.
	// #nosec G204 -- fixed argv.
	cmd := exec.CommandContext(ctx, talosctl, "validate", "--config", patched, "--mode", "cloud")

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
