// CrossGuard policy pack for hetzner-iac.
//
// WHY A POLICY PACK WHEN THE COMPONENTS ALREADY VALIDATE. Because component
// validation binds only the callers that go through the component, and this
// repository has a measured example. pkg/hetzner.BuildFirewallRules refuses an
// empty AdminCIDRs — ErrEmptyAdminCIDRs exists for exactly that — and then
// appends FirewallRuleOptions.Extra verbatim. Its per-rule validate() checks
// the protocol, the port and that the source list is non-empty. It does not
// look at what the sources ARE, so this passes today:
//
//	BuildFirewallRules([]string{"203.0.113.4/32"}, FirewallRuleOptions{
//	    Extra: []FirewallRule{{Protocol: "tcp", Port: "6443",
//	        SourceIPs: []string{"0.0.0.0/0"}}},
//	})
//
// A world-open kube-apiserver, accepted by the validator whose whole purpose is
// to refuse one. CrossGuard runs over the RESOURCES a program declares rather
// than the constructors it called, so it holds regardless of which code path
// produced them — including a layer that skips pkg/hetzner entirely.
//
// Written in Go via pulumi/policyx: the same language as the rest of the
// repository, so no Node or Python runtime is dragged into CI for it.
//
// ENFORCEMENT. `pulumi preview --policy-pack ./policy` is a local mode, which
// means a policy can be skipped by omitting the flag — it is a CI gate, not an
// unbypassable control. This repository's state lives in Pulumi Cloud, so the
// organisation policy-group mode IS available here and would make these
// mandatory for every stack without a flag to forget. Doing that is an
// organisation setting rather than a repository change, so it is noted in
// docs/configuration.md instead of pretended to here.
package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/blang/semver"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/policyx"
)

// The resource type tokens the policies match on. Spelled once: a typo here is
// a policy that inspects nothing and reports success, which is the one failure
// mode a policy pack must not have.
const (
	typeHcloudFirewall = "hcloud:index/firewall:Firewall"
	typeHcloudServer   = "hcloud:index/server:Server"
	typeHelmRelease    = "kubernetes:helm.sh/v3:Release"
)

// worldCIDR is "the whole internet" in the only spelling hcloud accepts for
// IPv4. The IPv6 form is checked beside it: a rule can be opened to either.
const (
	worldCIDR     = "0.0.0.0/0"
	worldCIDRIPv6 = "::/0"
)

// adminPorts are the ports that must never be reachable from the whole
// internet, and what each one would hand over.
//
// Derived from the same two constants pkg/hetzner opens in the baseline rule
// set — PortKubeAPI and PortTalosdAPI — but spelled here as strings because a
// policy reads them out of resource properties, where they are strings. The
// parity is held by a test rather than by an import: the policy pack is a
// separate program and importing the cluster package into it would pull the
// whole provider SDK into the policy plugin.
var adminPorts = map[string]string{
	"6443":  "the Kubernetes API",
	"50000": "the Talos API, which can apply machine config and reset a node",
}

func main() {
	if err := policyx.Main(newPolicyPack); err != nil {
		panic(err)
	}
}

//nolint:ireturn // policyx.NewPolicyPack returns the interface; so must this.
func newPolicyPack(_ *pulumi.Context) (policyx.PolicyPack, error) {
	pack, err := policyx.NewPolicyPack(
		"hetzner-iac",
		semver.MustParse("1.0.0"),
		policyx.EnforcementLevelMandatory,
		[]policyx.Policy{
			firewallAdminPortsNotWorldOpen(),
			serverJoinsPrivateNetwork(),
			helmReleasePinsVersion(),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("build hetzner-iac policy pack: %w", err)
	}

	return pack, nil
}

// firewallAdminPortsNotWorldOpen is the policy this pack exists for, and the
// one with a measured hole behind it — see the package comment.
//
//nolint:ireturn // policyx.ResourceValidationPolicy IS the SDK's policy type.
func firewallAdminPortsNotWorldOpen() policyx.ResourceValidationPolicy {
	return policyx.NewResourceValidationPolicy(
		"hcloud-admin-ports-not-world-open",
		policyx.ResourceValidationPolicyArgs{
			ConfigSchema: nil, // this policy takes no configuration
			Description: "hcloud firewalls must not expose the Kubernetes or Talos API " +
				"to the whole internet.",
			EnforcementLevel: policyx.EnforcementLevelMandatory,
			ValidateResource: func(_ context.Context, args policyx.ResourceValidationArgs) error {
				if args.Resource.Type != typeHcloudFirewall {
					return nil
				}

				for _, rule := range arrayOf(args.Resource.Properties, "rules") {
					if !rule.IsMap() {
						continue
					}

					for _, violation := range worldOpenAdminPorts(rule.AsMap()) {
						args.Manager.ReportViolation(violation, args.Resource.URN)
					}
				}

				return nil
			},
		})
}

// worldOpenAdminPorts is the judgement, separated from the SDK plumbing so it
// can be tested without running a policy plugin. It returns one message per
// world-open admin port in the rule, and nothing for a rule that is fine.
func worldOpenAdminPorts(rule property.Map) []string {
	port := stringOf(rule, "port")

	what, isAdminPort := adminPorts[port]
	if !isAdminPort {
		return nil
	}

	var out []string

	for _, source := range arrayOf(rule, "sourceIps") {
		if !source.IsString() {
			continue
		}

		cidr := source.AsString()
		if cidr != worldCIDR && cidr != worldCIDRIPv6 {
			continue
		}

		out = append(out, fmt.Sprintf(
			"firewall rule opens port %s (%s) to %s. Restrict it to network.adminCIDRs "+
				"in the topology: a world-open Talos API is a remote node reset, and a "+
				"world-open Kubernetes API is an authentication attempt away from the cluster.",
			port, what, cidr))
	}

	return out
}

// serverJoinsPrivateNetwork guards the assumption the whole design rests on.
//
// etcd advertises its peers on network.nodeSubnet and kubelet pins its node IP
// to the same subnet — both added when the control plane went to three members,
// because etcd on the public interface is filtered by the firewall and the
// members never form a quorum. A server created without a network attachment
// still joins the cluster, so nothing looks broken until something needs the
// private path.
//
//nolint:ireturn // policyx.ResourceValidationPolicy IS the SDK's policy type.
func serverJoinsPrivateNetwork() policyx.ResourceValidationPolicy {
	return policyx.NewResourceValidationPolicy(
		"hcloud-server-joins-private-network",
		policyx.ResourceValidationPolicyArgs{
			ConfigSchema:     nil, // this policy takes no configuration
			Description:      "hcloud servers must attach to the cluster's private network.",
			EnforcementLevel: policyx.EnforcementLevelMandatory,
			ValidateResource: func(_ context.Context, args policyx.ResourceValidationArgs) error {
				if args.Resource.Type != typeHcloudServer {
					return nil
				}

				if len(arrayOf(args.Resource.Properties, "networks")) > 0 {
					return nil
				}

				args.Manager.ReportViolation(
					"server has no private network attachment. etcd advertises its peers on "+
						"network.nodeSubnet and kubelet pins its node IP to it, so cluster traffic "+
						"would cross the public interface — where the firewall filters it and the "+
						"cloud controller manager writes no routes.",
					args.Resource.URN)

				return nil
			},
		})
}

// helmReleasePinsVersion catches a chart that never went through pkg/charts.
//
// That registry rejects a floating version with its own pattern, and
// tools/charts reports what is outdated — but both only see the charts declared
// in the registry. A Release built straight from the Kubernetes provider
// resolves to whatever the repository serves at apply time, so the same commit
// produces different clusters on different days.
//
//nolint:ireturn // policyx.ResourceValidationPolicy IS the SDK's policy type.
func helmReleasePinsVersion() policyx.ResourceValidationPolicy {
	return policyx.NewResourceValidationPolicy(
		"helm-release-pins-chart-version",
		policyx.ResourceValidationPolicyArgs{
			ConfigSchema:     nil, // this policy takes no configuration
			Description:      "Helm releases must pin an explicit chart version.",
			EnforcementLevel: policyx.EnforcementLevelMandatory,
			ValidateResource: func(_ context.Context, args policyx.ResourceValidationArgs) error {
				if args.Resource.Type != typeHelmRelease {
					return nil
				}

				if strings.TrimSpace(stringOf(args.Resource.Properties, "version")) != "" {
					return nil
				}

				args.Manager.ReportViolation(
					"Helm release pins no chart version: it resolves to whatever the repository "+
						"serves at apply time, so the same commit yields different clusters. Declare "+
						"the chart in pkg/charts, which is the version source of record.",
					args.Resource.URN)

				return nil
			},
		})
}

// stringOf reads a string property, and answers "" for a missing one or one of
// another type. A policy must not panic on a resource shape it did not expect:
// that fails the whole preview with a stack trace instead of a verdict.
func stringOf(m property.Map, key string) string {
	value, ok := m.GetOk(key)
	if !ok || !value.IsString() {
		return ""
	}

	return value.AsString()
}

// arrayOf reads an array property as a plain slice, and answers nil for a
// missing one or one of another type — same reason as stringOf.
func arrayOf(m property.Map, key string) []property.Value {
	value, ok := m.GetOk(key)
	if !ok || !value.IsArray() {
		return nil
	}

	array := value.AsArray()
	out := make([]property.Value, 0, array.Len())

	for _, item := range array.All {
		out = append(out, item)
	}

	return out
}
