package hetzner

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pulumi/pulumi-hcloud/sdk/go/hcloud"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// ValidateServerTypes checks every server type in the topology against the
// ones the account can actually create.
//
// It exists because the alternative is what happened: `pulumi up` created the
// network, the firewall, the placement group, the subnet and the Talos secrets
// — ten resources — and then died on the eleventh with
//
//	server type cx42 not found: provider=hcloud@1.41.0
//
// leaving a half-built cluster in state. The topology validator already moves
// a bad location to plan time for exactly this reason, with a comment saying
// so; server types were only checked for being non-empty.
//
// The list comes from the API rather than a table in this file. A hardcoded
// list would be wrong in the other direction: Hetzner adds and retires types,
// and a stale table rejects a type that works, which is worse than accepting
// one that does not.
func ValidateServerTypes(ctx *pulumi.Context, topology *Topology) error {
	wanted := wantedServerTypes(topology)
	if len(wanted) == 0 {
		return nil
	}

	available, err := hcloud.GetServerTypes(ctx)
	if err != nil {
		// Not fatal. A lookup that cannot run must not stop a deploy that
		// would otherwise work — the API is about to be called anyway, and it
		// will reject a bad type itself. This check buys an earlier, clearer
		// failure, not a new way to fail.
		_ = ctx.Log.Warn(fmt.Sprintf(
			"could not list server types, so they are unverified until apply: %v", err),
			&pulumi.LogArgs{Ephemeral: false})

		return nil
	}

	// Architecture per name, not just the names: a type can exist and still be
	// unbootable here. The baked Talos image is one architecture, and the cax
	// line is Arm while the rest are x86 — "cax31 not found" would be a lie,
	// and "cx43 not found" when it exists for the other architecture is worse.
	architecture := map[string]string{}
	for _, t := range available.ServerTypes {
		architecture[t.Name] = t.Architecture
	}

	// An empty list means the lookup told us nothing rather than that the
	// account can create nothing. Treating it as the latter would reject every
	// topology.
	if len(architecture) == 0 {
		return nil
	}

	want := topology.Talos.Architecture

	var missing, wrongArch []string

	for _, name := range wanted {
		arch, exists := architecture[name]

		switch {
		case !exists:
			missing = append(missing, name)
		case want != "" && arch != want:
			wrongArch = append(wrongArch, fmt.Sprintf("%s is %s", name, arch))
		}
	}

	if len(missing) == 0 && len(wrongArch) == 0 {
		return nil
	}

	var problems []string

	if len(missing) > 0 {
		problems = append(problems, fmt.Sprintf(
			"server type(s) %s do not exist in this Hetzner project",
			strings.Join(missing, ", ")))
	}

	if len(wrongArch) > 0 {
		problems = append(problems, fmt.Sprintf(
			"%s, but the baked Talos image is %s — a mismatched type will not boot",
			strings.Join(wrongArch, ", "), want))
	}

	return fmt.Errorf("%s.\nAvailable for %s: %s",
		strings.Join(problems, "; "), archLabel(want),
		strings.Join(namesFor(architecture, want), ", "))
}

// namesFor lists the types that can boot the given architecture, or every type
// when the topology does not say.
func namesFor(architecture map[string]string, want string) []string {
	set := map[string]bool{}

	for name, arch := range architecture {
		if want == "" || arch == want {
			set[name] = true
		}
	}

	return sortedNames(set)
}

func archLabel(want string) string {
	if want == "" {
		return "any architecture"
	}

	return want
}

// wantedServerTypes is every distinct type the topology asks for.
func wantedServerTypes(topology *Topology) []string {
	seen := map[string]bool{}

	for _, name := range append(
		[]string{topology.ControlPlane.ServerType},
		workerServerTypes(topology)...,
	) {
		if name != "" {
			seen[name] = true
		}
	}

	return sortedNames(seen)
}

func workerServerTypes(topology *Topology) []string {
	out := make([]string, 0, len(topology.WorkerPools))
	for _, pool := range topology.WorkerPools {
		out = append(out, pool.ServerType)
	}

	return out
}

func sortedNames(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}

	sort.Strings(out)

	return out
}
