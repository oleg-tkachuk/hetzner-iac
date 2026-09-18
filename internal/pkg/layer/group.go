package layer

import (
	"fmt"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/pulumiopts"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// Group is a set of components deployed under one parent component resource.
//
// It exists because two things that were separate Pulumi projects can now live
// in one, and "which of them does this resource belong to" then has to be
// answerable from the STATE rather than from the source. A group is a
// pulumi.ComponentResource, so its type lands in every child's URN:
//
//	urn:…::hetzner-iac:platform:ClusterServices$kubernetes:helm.sh/v3:Release::cert-manager
//	urn:…::hetzner-iac:platform:Ingress$kubernetes:helm.sh/v3:Release::traefik
//
// which is what lets one former layer be addressed on its own:
//
//	pulumi up      --target '**:Ingress$**'
//	pulumi destroy --target '**:Ingress$**' --target-dependents
//
// Without it the two sets are one flat list of releases under the stack, and
// the only thing telling them apart is a reader's memory of which chart used
// to be where.
//
// A group is not a dependency. Parenting decides the URN and the display tree;
// it does not make one child wait for another, and it does not propagate a
// change from the parent — which is the property that keeps components in the
// same group independent of each other. What DOES couple two components is
// After, and order refuses one that crosses a group boundary.
type Group struct {
	// Type is the Pulumi type token, in the pkg:module:Type shape Pulumi
	// expects. It is the part that appears in every child's URN, so it has to
	// be distinct per group: two groups sharing a type are indistinguishable
	// in the state, which is the whole thing this type exists to prevent.
	Type string

	// Name is the component resource's own name. It appears in the display
	// tree and in the group's own URN, not in its children's.
	Name string
}

// Empty answers whether a component named no group. A layer that has not been
// merged with another needs none, and passing one would put every resource a
// level deeper for no reason.
func (g Group) Empty() bool {
	return g == Group{}
}

// String identifies a group in an error message.
func (g Group) String() string {
	if g.Empty() {
		return "(no group)"
	}

	return fmt.Sprintf("%s (%s)", g.Name, g.Type)
}

// groupParent is the component resource a group's children hang from.
//
// It holds nothing and has no outputs of its own worth reading: its entire job
// is to put the group's type into its children's URNs. Registered with no
// resource options on purpose — a provider passed here would become the
// default for every child, and the children already carry their own, one of
// which is the Hetzner provider rather than the Kubernetes one.
type groupParent struct {
	pulumi.ResourceState

	// Components is what the group holds, by Key, in the order they were
	// deployed. Registered as the group's output so the state says what a
	// group contained at the last apply — the closest thing to a contract a
	// pure grouping node can offer, and enough to answer "what would
	// --target on this group have taken" from `pulumi stack --show-urns`.
	Components pulumi.StringArrayOutput `pulumi:"components"`
}

// parentFor registers a group's component resource, once per group per run.
func (r *Runner) parentFor(made map[Group]*groupParent, group Group) (*groupParent, error) {
	if parent, ok := made[group]; ok {
		return parent, nil
	}

	parent := &groupParent{}
	if err := r.Ctx.RegisterComponentResource(group.Type, group.Name, parent); err != nil {
		return nil, fmt.Errorf("register group %s: %w", group, err)
	}

	made[group] = parent

	return parent, nil
}

// inGroup returns the runner a grouped component's resources are created with.
//
// A COPY of the runner rather than a mutation of it, because one Runner is
// shared by every component and by the layer's own code around Deploy: a field
// set for one component would still be set for the next.
//
// The two options it adds are the whole mechanism:
//
//   - Parent, which is what puts the group's type in the URN.
//   - Aliases with NoParent, which is what stops that URN change from being
//     read as a different resource. Without it Pulumi sees the old flat URN
//     gone and a new one arrived, and plans a delete and a create for every
//     release in the layer — measured on the first preview of this merge. For
//     a Helm release that is an uninstall and a reinstall of everything the
//     cluster runs, which is exactly what the merge was supposed not to cost.
//
// Both ride in Options, which the Runner already documents as the way a layer
// passes the provider through without each component remembering to attach it.
// A Create function that builds its resources with r.With picks them up with no
// change of its own.
func (r *Runner) inGroup(parent *groupParent) *Runner {
	scoped := *r
	scoped.Options = pulumiopts.With(r.Options,
		pulumi.Parent(parent),
		pulumi.Aliases([]pulumi.Alias{{NoParent: pulumi.Bool(true)}}),
	)

	return &scoped
}

// groupsOf indexes a set by group, preserving the order components appear in.
func groupsOf(components Components) map[Group][]string {
	held := map[Group][]string{}

	for _, component := range components {
		if component.Group.Empty() {
			continue
		}

		held[component.Group] = append(held[component.Group], component.Key())
	}

	return held
}
