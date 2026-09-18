package hetzner

import (
	"fmt"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"

	"github.com/pulumi/pulumi-hcloud/sdk/go/hcloud"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// Firewall is the cluster's public-interface perimeter.
//
// It attaches by LABEL SELECTOR rather than by naming servers. That is what
// makes a node created or replaced later inherit the perimeter with no diff on
// the firewall itself — and it closes the window in which a freshly created
// node is briefly unprotected.
type Firewall struct {
	pulumi.ResourceState

	Firewall *hcloud.Firewall `pulumi:"-"`

	FirewallID pulumi.IntOutput `pulumi:"firewallId"`
}

// FirewallArgs configures the perimeter.
type FirewallArgs struct {
	ClusterName string
	AdminCIDRs  []string
	AllowICMP   bool
	ExtraRules  []clusterspec.FirewallRule
}

// NewFirewall provisions the cluster perimeter.
func NewFirewall(ctx *pulumi.Context, name string, args *FirewallArgs, opts ...pulumi.ResourceOption) (*Firewall, error) {
	if args == nil {
		return nil, fmt.Errorf("NewFirewall(%s): args must not be nil", name)
	}

	rules, err := clusterspec.BuildFirewallRules(args.AdminCIDRs, clusterspec.FirewallRuleOptions{
		AllowICMP: args.AllowICMP,
		Extra:     args.ExtraRules,
	})
	if err != nil {
		return nil, err
	}

	component := &Firewall{}
	if registerErr := ctx.RegisterComponentResource(typeFirewall, name, component, opts...); registerErr != nil {
		return nil, fmt.Errorf("register %s: %w", typeFirewall, registerErr)
	}

	ruleArray := make(hcloud.FirewallRuleArray, 0, len(rules))

	for _, rule := range rules {
		ruleArgs := &hcloud.FirewallRuleArgs{
			Description: pulumi.String(rule.Description),
			Direction:   pulumi.String(clusterspec.DirectionIn),
			Protocol:    pulumi.String(rule.Protocol),
			SourceIps:   toStringArray(rule.SourceIPs),
		}

		// hcloud rejects a port on protocols that have none, so the field is
		// left unset rather than sent empty.
		if rule.Port != "" {
			ruleArgs.Port = pulumi.String(rule.Port)
		}

		ruleArray = append(ruleArray, ruleArgs)
	}

	firewall, err := hcloud.NewFirewall(ctx, name, &hcloud.FirewallArgs{
		Name:   pulumi.String(args.ClusterName),
		Labels: toStringMap(clusterspec.ResourceLabels(args.ClusterName, nil)),
		Rules:  ruleArray,
		ApplyTos: hcloud.FirewallApplyToArray{
			&hcloud.FirewallApplyToArgs{
				LabelSelector: pulumi.String(clusterspec.ClusterSelector(args.ClusterName)),
			},
		},
	}, pulumi.Parent(component))
	if err != nil {
		return nil, fmt.Errorf("hcloud firewall: %w", err)
	}

	component.Firewall = firewall
	component.FirewallID = idToInt(firewall.ID())

	if err := ctx.RegisterResourceOutputs(component, pulumi.Map{
		"firewallId": component.FirewallID,
	}); err != nil {
		return nil, fmt.Errorf("register firewall outputs: %w", err)
	}

	return component, nil
}
