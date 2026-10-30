// Command nodes prints the public IPv4 address of every control-plane server
// of one cluster, one per line, sorted by server name.
//
// From Hetzner rather than from the cluster: cluster:etcd:restore runs when
// etcd is what is broken, and `talosctl get members` needs etcd to answer.
// Through hcloud-go rather than the hcloud CLI's columns, which print a
// placeholder for a server with no public IPv4 — a value the restore would
// then have targeted as an address.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/hcloudtoken"
)

const timeout = 30 * time.Second

var errUsage = errors.New("usage: nodes <stack> <cluster-name>")

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(parent context.Context, args []string) error {
	if len(args) != 2 || args[0] == "" || args[1] == "" {
		return errUsage
	}

	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	stack, cluster := args[0], args[1]

	token, err := hcloudtoken.Token(ctx, stack)
	if err != nil {
		return err
	}

	selector := clusterspec.ControlPlaneSelector(cluster)

	servers, err := hcloud.NewClient(hcloud.WithToken(token)).Server.AllWithOpts(ctx, hcloud.ServerListOpts{
		ListOpts: hcloud.ListOpts{LabelSelector: selector},
	})
	if err != nil {
		return fmt.Errorf("list servers matching %s: %w", selector, err)
	}

	found, err := addresses(servers)
	if err != nil {
		return fmt.Errorf("%s: %w", selector, err)
	}

	fmt.Println(strings.Join(found, "\n"))

	return nil
}

// addresses is each server's public IPv4, sorted by server name.
//
// None is an error, not an empty list: a wrong cluster name or token matches
// nothing, and a loop over nothing reports success. So is a server without a
// public IPv4 — network.publicIPv4: false is a supported topology, and its
// nodes are not reachable from here at an address this could print.
func addresses(servers []*hcloud.Server) ([]string, error) {
	if len(servers) == 0 {
		return nil, errors.New("no control-plane server matches")
	}

	sorted := slices.SortedFunc(slices.Values(servers), func(a, b *hcloud.Server) int {
		return strings.Compare(a.Name, b.Name)
	})

	out := make([]string, 0, len(sorted))

	for _, server := range sorted {
		ip := server.PublicNet.IPv4.IP
		if ip == nil || ip.IsUnspecified() {
			return nil, fmt.Errorf("server %s has no public IPv4 address to reach it at", server.Name)
		}

		out = append(out, ip.String())
	}

	return out, nil
}
