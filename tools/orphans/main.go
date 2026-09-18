// Command orphans reports Hetzner resources that nothing in the cluster
// claims any more.
//
// It exists because the two ways this platform loses track of a resource are
// both silent and both billed:
//
//   - A StatefulSet's PersistentVolumeClaims outlive their Helm release. That
//     is Kubernetes behaving as designed — volumeClaimTemplates survive an
//     upgrade so it cannot eat the data — but it means destroying a layer
//     leaves its volumes, and 160 GiB were found that way.
//   - Deleting a cluster deletes the API server that would have told the CSI
//     driver to remove a volume, so the volume stays with nothing left
//     anywhere that refers to it.
//
// Read-only, and deliberately: it prints and exits non-zero, and never
// deletes. A volume whose PersistentVolume is gone still holds the data that
// was on it, and this check cannot know whether that matters.
package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/hcloudtoken"
)

// timeout covers five Hetzner API calls and three kubectl calls on a slow link.
const timeout = 2 * time.Minute

func main() {
	clean, err := run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Exiting here rather than inside run: os.Exit does not run deferred
	// calls, so exiting there would leak the context's cancel — and an
	// orphan-reporting tool leaving something behind is a poor joke.
	if !clean {
		// Non-zero, so this can gate: an orphan is a bill, and a check that
		// reports one and exits zero is a check nobody notices.
		os.Exit(1)
	}
}

func run() (clean bool, err error) {
	if len(os.Args) != 4 {
		return false, fmt.Errorf("usage: orphans <stack> <kubeconfig> <topology>")
	}

	stack, kubeconfig, topologyPath := os.Args[1], os.Args[2], os.Args[3]

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	token, err := hcloudtoken.Token(ctx, stack)
	if err != nil {
		return false, err
	}

	topology, err := clusterspec.LoadTopology(topologyPath)
	if err != nil {
		return false, fmt.Errorf("read the pinned Talos version: %w", err)
	}

	inventory, err := readInventory(ctx, token)
	if err != nil {
		return false, err
	}

	// Ask the cluster only when it can exist.
	//
	// readClaims fails on an unreachable cluster rather than returning an
	// empty set, because empty claims would report every volume in the
	// project as an orphan. That is right while the cluster's servers are
	// still there — and wrong once they are not, which is exactly when an
	// operator runs this check: straight after a teardown, to see what it
	// left behind. The old behaviour was to refuse, and the refusal named
	// the reason for a rule that no longer applied.
	claims := Claims{
		PersistentVolumes: map[string]bool{},
		ServiceUIDs:       map[string]bool{},
		Nodes:             map[string]bool{},
	}

	note := ClusterGoneNote

	if servers := ClusterServers(inventory, topology.Metadata.Name); len(servers) > 0 {
		note = ""

		claims, err = readClaims(ctx, kubeconfig)
		if err != nil {
			return false, fmt.Errorf("%w\n\n%d server(s) still carry %s=%s, so the cluster "+
				"should answer. If it is gone, its servers are not — and they are billed",
				err, len(servers), clusterspec.LabelCluster, topology.Metadata.Name)
		}
	}

	claims.TalosVersion = topology.Talos.Version

	found := Orphans(inventory, claims)

	fmt.Print(Report(found, inventory.Examined(), note))

	return len(found) == 0, nil
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
func readInventory(ctx context.Context, token string) (Inventory, error) {
	var inventory Inventory

	client := hcloud.NewClient(hcloud.WithToken(token))

	volumes, err := client.Volume.All(ctx)
	if err != nil {
		return inventory, fmt.Errorf("list volumes: %w", err)
	}

	for _, volume := range volumes {
		inventory.Volumes = append(inventory.Volumes, Volume{
			Name: volume.Name, SizeGB: volume.Size,
		})
	}

	balancers, err := client.LoadBalancer.All(ctx)
	if err != nil {
		return inventory, fmt.Errorf("list load balancers: %w", err)
	}

	for _, balancer := range balancers {
		inventory.LoadBalancers = append(inventory.LoadBalancers, LoadBalancer{
			Name: balancer.Name, Labels: balancer.Labels,
		})
	}

	servers, err := client.Server.All(ctx)
	if err != nil {
		return inventory, fmt.Errorf("list servers: %w", err)
	}

	for _, server := range servers {
		found := Server{Name: server.Name, Labels: server.Labels}
		if server.ServerType != nil {
			found.Type = server.ServerType.Name
		}

		inventory.Servers = append(inventory.Servers, found)
	}

	addresses, err := client.PrimaryIP.All(ctx)
	if err != nil {
		return inventory, fmt.Errorf("list primary ips: %w", err)
	}

	for _, address := range addresses {
		inventory.PrimaryIPs = append(inventory.PrimaryIPs, PrimaryIP{
			Name: address.Name, IP: address.IP.String(), AssigneeID: address.AssigneeID,
		})
	}

	// Snapshots only. The project also sees every public system image, and
	// reporting Debian as an orphan would make the list unreadable.
	snapshots, err := client.Image.AllWithOpts(ctx, hcloud.ImageListOpts{
		Type: []hcloud.ImageType{hcloud.ImageTypeSnapshot},
	})
	if err != nil {
		return inventory, fmt.Errorf("list snapshots: %w", err)
	}

	for _, snapshot := range snapshots {
		inventory.Snapshots = append(inventory.Snapshots, Snapshot{
			Description: snapshot.Description, SizeGB: float64(snapshot.ImageSize), Labels: snapshot.Labels,
		})
	}

	return inventory, nil
}

// readClaims asks the cluster what it is using.
func readClaims(ctx context.Context, kubeconfig string) (Claims, error) {
	claims := Claims{
		PersistentVolumes: map[string]bool{},
		ServiceUIDs:       map[string]bool{},
		Nodes:             map[string]bool{},
	}

	// A PersistentVolume's name is the hcloud volume's name, which is what
	// makes this comparable at all.
	volumes, err := kubectlNames(ctx, kubeconfig, "persistentvolumes", "{range .items[*]}{.metadata.name}{\"\\n\"}{end}")
	if err != nil {
		return claims, err
	}

	for _, name := range volumes {
		claims.PersistentVolumes[name] = true
	}

	uids, err := kubectlNames(ctx, kubeconfig, "services", "{range .items[*]}{.metadata.uid}{\"\\n\"}{end}")
	if err != nil {
		return claims, err
	}

	for _, uid := range uids {
		claims.ServiceUIDs[uid] = true
	}

	nodes, err := kubectlNames(ctx, kubeconfig, "nodes", "{range .items[*]}{.metadata.name}{\"\\n\"}{end}")
	if err != nil {
		return claims, err
	}

	for _, name := range nodes {
		claims.Nodes[name] = true
	}

	return claims, nil
}

// kubectlNames lists one field of one resource kind, across all namespaces.
//
// An unreachable cluster is an error rather than an empty list, and the
// difference is the whole point: an empty list would report every volume in
// the project as an orphan and invite an operator to delete them.
func kubectlNames(ctx context.Context, kubeconfig, kind, template string) ([]string, error) {
	// #nosec G204,G702 -- kind and template are literals from this file, the
	// kubeconfig path comes from the Taskfile, and CommandContext takes an
	// argument vector: there is no shell to interpret any of it. The taint
	// analysis cannot see that, the same way it cannot in tools/stack.
	cmd := exec.CommandContext(ctx, "kubectl", "--kubeconfig", kubeconfig,
		"get", kind, "--all-namespaces", "-o", "jsonpath="+template)

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("kubectl get %s: %w: %s\n"+
			"the cluster has to answer: an empty list would report every resource in the\n"+
			"project as an orphan", kind, err, strings.TrimSpace(stderr.String()))
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
