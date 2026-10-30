// Command orphans reports Hetzner resources that nothing claims any more:
// no stack's state holds them and nothing in the cluster uses them.
//
// It exists because the ways this platform loses track of a resource are all
// silent, and most are billed:
//
//   - A StatefulSet's PersistentVolumeClaims outlive their Helm release. That
//     is Kubernetes behaving as designed — volumeClaimTemplates survive an
//     upgrade so it cannot eat the data — but it means destroying a layer
//     leaves its volumes, and 160 GiB were found that way.
//   - Deleting a cluster deletes the API server that would have told the CSI
//     driver to remove a volume, so the volume stays with nothing left
//     anywhere that refers to it.
//   - A destroy that stops part-way, or a stack removed with --force, leaves
//     resources no state holds; and anything made by hand in the console was
//     never held by one.
//
// Read-only, and deliberately: it prints and exits non-zero, and never
// deletes. A volume whose PersistentVolume is gone still holds the data that
// was on it, and this check cannot know whether that matters.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/hcloudtoken"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/pulumilogin"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/report"
)

// timeout covers a stack export per project and environment, a listing per
// Hetzner resource kind and four kubectl calls, on a slow link.
const timeout = 5 * time.Minute

// The arguments before the project directories.
const fixedArgs = 3

const usage = "usage: orphans <stack> <kubeconfig> <topology> <project dir> [<project dir> …]"

// errUsage is the refusal of too few arguments.
var errUsage = errors.New(usage)

// The process's exit codes when run returns no error.
const (
	exitClean   = 0
	exitOrphans = 1
)

func main() {
	clean, err := run(os.Args[1:], liveDeps())
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Exiting here rather than inside run: os.Exit does not run deferred
	// calls, so exiting there would leak the context's cancel — and an
	// orphan-reporting tool leaving something behind is a poor joke.
	os.Exit(exitCode(clean))
}

// exitCode is non-zero when anything was found, so this can gate: an orphan
// is a bill, and a check that reports one and exits zero is a check nobody
// notices.
func exitCode(clean bool) int {
	if clean {
		return exitClean
	}

	return exitOrphans
}

// deps are what run reads from outside the process, so a test can stand in
// for each one.
type deps struct {
	requireLogin func(dirs []string) error
	token        func(ctx context.Context, stack string) (string, error)
	topology     func(path string) (*clusterspec.Topology, error)
	inventory    func(ctx context.Context, token string) (Inventory, error)
	held         func(ctx context.Context, dirs []string) (Held, error)
	claims       func(ctx context.Context, kubeconfig string) (Claims, error)
	out          io.Writer
	paint        report.Painter
}

// liveDeps are the real backend, Hetzner API, cluster and terminal.
func liveDeps() deps {
	return deps{
		requireLogin: pulumilogin.Require,
		token:        hcloudtoken.Token,
		topology:     clusterspec.LoadTopology,
		inventory: func(ctx context.Context, token string) (Inventory, error) {
			return readInventory(ctx, hcloud.NewClient(hcloud.WithToken(token)))
		},
		held: func(ctx context.Context, dirs []string) (Held, error) {
			return readHeld(ctx, dirs, openWorkspace)
		},
		claims: func(ctx context.Context, kubeconfig string) (Claims, error) {
			return readClaims(ctx, kubeconfig, execCommand)
		},
		out:   os.Stdout,
		paint: report.PainterFor(os.Stdout),
	}
}

// invocation is the command line, named.
type invocation struct {
	stack, kubeconfig, topology string
	dirs                        []string
}

// parseArgs names the command line's arguments.
func parseArgs(args []string) (invocation, error) {
	if len(args) <= fixedArgs {
		return invocation{}, errUsage
	}

	return invocation{stack: args[0], kubeconfig: args[1], topology: args[2], dirs: args[fixedArgs:]}, nil
}

func run(args []string, d deps) (clean bool, err error) {
	inv, err := parseArgs(args)
	if err != nil {
		return false, err
	}

	err = d.requireLogin(inv.dirs)
	if err != nil {
		return false, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	seen, err := observe(ctx, inv, d)
	if err != nil {
		return false, err
	}

	servers := ClusterServers(seen.inventory, seen.topology.Metadata.Name)

	claims, err := clusterClaims(ctx, inv.kubeconfig, servers, seen, d.claims)
	if err != nil {
		return false, err
	}

	found := Orphans(seen.inventory, claims)
	header := Header{
		Stack: inv.stack, Cluster: seen.topology.Metadata.Name, ClusterGone: len(servers) == 0,
		Stacks: seen.held.Stacks,
	}

	// Discarded as fmt.Print's always was: the exit code still says whether
	// anything was found, and that is what a gate reads.
	_, _ = fmt.Fprint(d.out, Report(header, seen.inventory, found, d.paint))

	return len(found) == 0, nil
}

// observed is what the token, the topology, the Hetzner project and the
// stacks' states say before the cluster is asked.
type observed struct {
	topology  *clusterspec.Topology
	inventory Inventory
	held      Held
}

// observe reads everything but the cluster, in the order run always has.
func observe(ctx context.Context, inv invocation, d deps) (observed, error) {
	var seen observed

	token, err := d.token(ctx, inv.stack)
	if err != nil {
		return seen, err
	}

	seen.topology, err = d.topology(inv.topology)
	if err != nil {
		return seen, fmt.Errorf("read the pinned Talos version: %w", err)
	}

	seen.inventory, err = d.inventory(ctx, token)
	if err != nil {
		return seen, err
	}

	seen.held, err = d.held(ctx, inv.dirs)
	if err != nil {
		return seen, fmt.Errorf("read the stacks' states: %w", err)
	}

	return seen, nil
}

// clusterClaims asks the cluster only when it can exist.
//
// readClaims fails on an unreachable cluster rather than returning an empty
// set, because empty claims would report every volume in the project as an
// orphan. That is right while the cluster's servers are still there — and
// wrong once they are not, which is exactly when an operator runs this check:
// straight after a teardown, to see what it left behind. The old behaviour was
// to refuse, and the refusal named the reason for a rule that no longer
// applied.
func clusterClaims(
	ctx context.Context, kubeconfig string, servers []Server, seen observed,
	read func(ctx context.Context, kubeconfig string) (Claims, error),
) (Claims, error) {
	claims := NewClaims()

	err := RefuseEmptyBackend(len(servers), seen.held.Stacks)
	if err != nil {
		return claims, err
	}

	if len(servers) > 0 {
		claims, err = read(ctx, kubeconfig)
		if err != nil {
			return claims, fmt.Errorf("%w\n\n%d server(s) still carry %s=%s, so the cluster "+
				"should answer. If it is gone, its servers are not — and they are billed",
				err, len(servers), clusterspec.LabelCluster, seen.topology.Metadata.Name)
		}
	}

	claims.TalosVersion = seen.topology.Talos.Version
	claims.Held = seen.held

	return claims, nil
}

// readInventory asks Hetzner what the project holds.
//
// The official SDK rather than `hcloud … -o json`: the field names then come
// from the library's own structs instead of from measurement. That is not a
// theoretical gain — the first version of this file decoded every volume size
// as zero, because the API calls the field `size` while the struct called it
// SizeGB, and nothing failed. It also drops a prerequisite: the hcloud CLI no
// longer has to be installed for this check to run.
//
// All* rather than a paged loop: the library walks the pages, and a project
// with more than one page of volumes is exactly the project that needs this.
func readInventory(ctx context.Context, client *hcloud.Client) (Inventory, error) {
	var inventory Inventory

	for _, read := range inventoryReads(client, &inventory) {
		if err := read(ctx); err != nil {
			return inventory, err
		}
	}

	return inventory, nil
}

// snapshotsOnly lists snapshots alone. The project also sees every public
// system image, and reporting Debian as an orphan would make the list
// unreadable.
func snapshotsOnly() hcloud.ImageListOpts {
	return hcloud.ImageListOpts{Type: []hcloud.ImageType{hcloud.ImageTypeSnapshot}}
}

// inventoryReads are readInventory's listings, in order, each filling one
// field of into.
func inventoryReads(client *hcloud.Client, into *Inventory) []func(context.Context) error {
	return []func(context.Context) error{
		func(ctx context.Context) (err error) {
			into.Volumes, err = listAs(ctx, "volumes", client.Volume.All, toVolume)

			return err
		},
		func(ctx context.Context) (err error) {
			into.LoadBalancers, err = listAs(ctx, "load balancers", client.LoadBalancer.All, toLoadBalancer)

			return err
		},
		func(ctx context.Context) (err error) {
			into.Servers, err = listAs(ctx, "servers", client.Server.All, toServer)

			return err
		},
		func(ctx context.Context) (err error) {
			into.PrimaryIPs, err = listAs(ctx, "primary ips", client.PrimaryIP.All, toPrimaryIP)

			return err
		},
		func(ctx context.Context) (err error) {
			snapshots := func(ctx context.Context) ([]*hcloud.Image, error) {
				return client.Image.AllWithOpts(ctx, snapshotsOnly())
			}
			into.Snapshots, err = listAs(ctx, "snapshots", snapshots, toSnapshot)

			return err
		},
		func(ctx context.Context) (err error) {
			into.Resources, err = readResources(ctx, client)

			return err
		},
	}
}

// listAs lists one kind through the SDK and converts each item to this
// check's own shape. The error names the kind as `list <what>`.
func listAs[T, U any](
	ctx context.Context, what string, all func(context.Context) ([]T, error), as func(T) U,
) ([]U, error) {
	items, err := all(ctx)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", what, err)
	}

	var converted []U
	for _, item := range items {
		converted = append(converted, as(item))
	}

	return converted, nil
}

func toVolume(volume *hcloud.Volume) Volume {
	return Volume{ID: volume.ID, Name: volume.Name, SizeGB: volume.Size}
}

func toLoadBalancer(balancer *hcloud.LoadBalancer) LoadBalancer {
	return LoadBalancer{ID: balancer.ID, Name: balancer.Name, Labels: balancer.Labels}
}

func toServer(server *hcloud.Server) Server {
	found := Server{ID: server.ID, Name: server.Name, Labels: server.Labels}
	if server.ServerType != nil {
		found.Type = server.ServerType.Name
	}

	return found
}

func toPrimaryIP(address *hcloud.PrimaryIP) PrimaryIP {
	return PrimaryIP{ID: address.ID, Name: address.Name, IP: address.IP.String(), AssigneeID: address.AssigneeID}
}

func toSnapshot(snapshot *hcloud.Image) Snapshot {
	return Snapshot{
		ID: snapshot.ID, Description: snapshot.Description, SizeGB: float64(snapshot.ImageSize),
		Labels: snapshot.Labels,
	}
}

// readResources lists the kinds only a stack can claim.
func readResources(ctx context.Context, client *hcloud.Client) ([]Resource, error) {
	var resources []Resource

	for _, read := range stackOnlyReads(client) {
		found, err := read(ctx)
		if err != nil {
			return nil, err
		}

		resources = append(resources, found...)
	}

	return resources, nil
}

// stackOnlyReads are readResources' listings, in order: floating IPs and
// networks, firewalls and placement groups, SSH keys, certificates and DNS
// zones, then storage boxes with their subaccounts.
func stackOnlyReads(client *hcloud.Client) []func(context.Context) ([]Resource, error) {
	return []func(context.Context) ([]Resource, error){
		func(ctx context.Context) ([]Resource, error) {
			return listAs(ctx, "floating ips", client.FloatingIP.All, func(address *hcloud.FloatingIP) Resource {
				return Resource{KindFloatingIP, address.ID, address.Name, address.Labels}
			})
		},
		func(ctx context.Context) ([]Resource, error) {
			return listAs(ctx, "networks", client.Network.All, func(network *hcloud.Network) Resource {
				return Resource{KindNetwork, network.ID, network.Name, network.Labels}
			})
		},
		func(ctx context.Context) ([]Resource, error) {
			return listAs(ctx, "firewalls", client.Firewall.All, func(firewall *hcloud.Firewall) Resource {
				return Resource{KindFirewall, firewall.ID, firewall.Name, firewall.Labels}
			})
		},
		func(ctx context.Context) ([]Resource, error) {
			return listAs(ctx, "placement groups", client.PlacementGroup.All, func(group *hcloud.PlacementGroup) Resource {
				return Resource{KindPlacementGroup, group.ID, group.Name, group.Labels}
			})
		},
		func(ctx context.Context) ([]Resource, error) {
			return listAs(ctx, "ssh keys", client.SSHKey.All, func(key *hcloud.SSHKey) Resource {
				return Resource{KindSSHKey, key.ID, key.Name, key.Labels}
			})
		},
		func(ctx context.Context) ([]Resource, error) {
			return listAs(ctx, "certificates", client.Certificate.All, func(certificate *hcloud.Certificate) Resource {
				return Resource{KindCertificate, certificate.ID, certificate.Name, certificate.Labels}
			})
		},
		func(ctx context.Context) ([]Resource, error) {
			return listAs(ctx, "dns zones", client.Zone.All, func(zone *hcloud.Zone) Resource {
				return Resource{KindZone, zone.ID, zone.Name, zone.Labels}
			})
		},
		func(ctx context.Context) ([]Resource, error) {
			return readStorageBoxes(ctx, client)
		},
	}
}

// readStorageBoxes lists storage boxes and each one's subaccounts.
func readStorageBoxes(ctx context.Context, client *hcloud.Client) ([]Resource, error) {
	var resources []Resource

	boxes, err := client.StorageBox.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list storage boxes: %w", err)
	}

	for _, box := range boxes {
		resources = append(resources, Resource{KindStorageBox, box.ID, box.Name, box.Labels})

		subaccounts, err := client.StorageBox.AllSubaccounts(ctx, box)
		if err != nil {
			return nil, fmt.Errorf("list subaccounts of storage box %s: %w", box.Name, err)
		}

		for _, subaccount := range subaccounts {
			resources = append(resources, Resource{KindSubaccount, subaccount.ID, subaccount.Username, subaccount.Labels})
		}
	}

	return resources, nil
}

// kubectl is the program readClaims asks the cluster through.
const kubectl = "kubectl"

// The resource kinds readClaims lists, as kubectl spells them.
const (
	kubePersistentVolumes = "persistentvolumes"
	kubeServices          = "services"
	kubeNodes             = "nodes"
)

// The jsonpath templates readClaims lists with: one field per line.
const (
	namesTemplate = "{range .items[*]}{.metadata.name}{\"\\n\"}{end}"
	uidsTemplate  = "{range .items[*]}{.metadata.uid}{\"\\n\"}{end}"
	// releasedTemplate filters by phase in the template rather than in a
	// second pass in Go, because kubectl is already the thing that can
	// answer it.
	releasedTemplate = "{range .items[?(@.status.phase==\"" + PhaseReleased + "\")]}{.metadata.name}{\"\\n\"}{end}"
)

// commandRunner runs a program from an argument vector and returns what it
// wrote to its standard output and standard error.
type commandRunner func(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)

// execCommand is the commandRunner that starts a process.
func execCommand(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error) {
	// #nosec G204,G702 -- name and args are literals from this file, the
	// kubeconfig path comes from the Taskfile, and CommandContext takes an
	// argument vector: there is no shell to interpret any of it. The taint
	// analysis cannot see that the vector form is the mitigation.
	cmd := exec.CommandContext(ctx, name, args...)

	var errOut bytes.Buffer

	cmd.Stderr = &errOut

	stdout, err = cmd.Output()

	return stdout, errOut.Bytes(), err
}

// claimQuery is one kubectl listing and the set of Claims it fills.
type claimQuery struct {
	kind, template string
	into           map[string]bool
}

// claimQueries are readClaims' listings, in order.
//
// A PersistentVolume's name is the hcloud volume's name, which is what makes
// the first comparable at all.
func claimQueries(claims Claims) []claimQuery {
	return []claimQuery{
		{kubePersistentVolumes, namesTemplate, claims.PersistentVolumes},
		{kubePersistentVolumes, releasedTemplate, claims.ReleasedVolumes},
		{kubeServices, uidsTemplate, claims.ServiceUIDs},
		{kubeNodes, namesTemplate, claims.Nodes},
	}
}

// readClaims asks the cluster what it is using.
func readClaims(ctx context.Context, kubeconfig string, run commandRunner) (Claims, error) {
	claims := NewClaims()

	for _, query := range claimQueries(claims) {
		names, err := kubectlNames(ctx, run, kubeconfig, query.kind, query.template)
		if err != nil {
			return claims, err
		}

		for _, name := range names {
			query.into[name] = true
		}
	}

	return claims, nil
}

// kubectlNames lists one field of one resource kind, across all namespaces.
//
// An unreachable cluster is an error rather than an empty list, and the
// difference is the whole point: an empty list would report every volume in
// the project as an orphan and invite an operator to delete them.
func kubectlNames(ctx context.Context, run commandRunner, kubeconfig, kind, template string) ([]string, error) {
	raw, stderr, err := run(ctx, kubectl, "--kubeconfig", kubeconfig,
		"get", kind, "--all-namespaces", "-o", "jsonpath="+template)
	if err != nil {
		return nil, fmt.Errorf("kubectl get %s: %w: %s\n"+
			"the cluster has to answer: an empty list would report every resource in the\n"+
			"project as an orphan", kind, err, strings.TrimSpace(string(stderr)))
	}

	return nonEmptyLines(string(raw)), nil
}

// nonEmptyLines splits jsonpath output, which ends with a trailing newline
// and is empty when nothing matched.
func nonEmptyLines(out string) []string {
	var lines []string

	for _, line := range strings.Split(out, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}

	return lines
}
