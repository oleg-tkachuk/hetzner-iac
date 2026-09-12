package layer

import (
	"fmt"
	"slices"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/charts"

	helm "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/helm/v3"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// A layer's components as data rather than as a sequence of calls.
//
// Ten of the twelve charts this platform installs are exactly "a pinned chart
// plus the values it needs", and written out longhand each one is the same
// eight lines with a different values function. As a table the eight lines
// happen once, and what is left per component is the part that differs.
//
// The reason worth stating is not brevity. A sequence of calls cannot be
// enumerated, so nothing can ask "is every pinned chart deployed by exactly
// one component" or "does every component have workloads declared" — those
// were checked by reading, per layer. A table can be walked by a test.
//
// Ordering is the other half. It used to live in the order calls appeared and,
// between layers, in directory names — which is how the cloud controller
// manager came to be applied before the CNI and sat Pending for a ten-minute
// timeout. Here a component names what it follows and the engine enforces it.
//
// There is deliberately no version on the set. A version needs a boundary two
// sides can sit on opposite ends of, and the only candidate — a deployed stack
// against the checkout you are reading — is already answered better: Pulumi
// records git.head, git.dirty and the branch with every update, and
// `pulumi preview` says exactly whether the cluster matches the code. A
// hand-bumped integer would restate that less precisely and go stale silently.

// Components is a layer's deployable set: everything it installs, and what
// each piece must follow.
//
// A named type rather than a bare []Component so the concept has somewhere to
// be documented and something to search for. To add a component to a layer,
// add an entry to that layer's Components; to make it wait for another, name
// that other component in After. pkg/layer/layertest.Check then holds the set
// to its invariants — pinned chart, no cycle, workloads declared — without the
// layer writing a test for each.
//
// Most components are a Helm release, which is what Chart and Values are for.
// Two in this repository are not — a Secret both hcloud charts read, and the
// ClusterIssuer cert-manager's CRD makes possible — and those set Create
// instead.
//
// They are components rather than a hook around the set, and that is the whole
// design decision. A hook that ran after the releases would have fitted the
// ClusterIssuer and NOT the Secret, which sits between Cilium and the two
// charts that consume it. Anything with a place in the dependency order has to
// be IN the order, or the order is not the thing it claims to be.
type Components []Component

// Component is one deployable piece of a layer: a Helm release, or a resource
// that belongs in the same dependency order as the releases.
type Component struct {
	// Name is what other components use in After. Empty takes Chart, which is
	// what a release wants; a Create component has to set it.
	Name string

	// Chart is a key into pkg/charts. Set it for a Helm release, and leave
	// Create nil.
	Chart string

	// Create makes a resource that is not a release, given the resources this
	// component named in After. Set it for anything that is not a chart, and
	// leave Chart empty.
	//
	// Returning (nil, nil) is how an optional feature declines: the component
	// stays in the set, so it is still enumerated and still ordered, and
	// nothing is created. That keeps the set static and readable rather than
	// assembled behind an `if` where a test cannot see it.
	Create func(*Runner, []pulumi.Resource) (pulumi.Resource, error)

	// Release overrides the Helm release name. Empty uses the chart key.
	Release string

	// TimeoutSeconds overrides the default for a chart that is genuinely slow.
	TimeoutSeconds int

	// After names the components this one must follow, by Name. It becomes a
	// DependsOn, so it is a fact the engine holds rather than a convention the
	// reader has to keep.
	After []string

	// Values builds the chart values. A function rather than a map because
	// values are the part that genuinely differs: they read stack config and
	// the cluster tier's outputs, both of which exist only at run time.
	//
	// Nil means the chart's own defaults, which is what a values-free
	// component wants — restating a default is a diff that renders identically.
	Values func(*Runner) pulumi.Map

	// SkipCRDs leaves custom resource definitions alone.
	SkipCRDs bool
}

// Key is the name other components refer to this one by: Name, or the chart
// key when Name is empty. Exported because pkg/layer/layertest asserts the
// order and has to ask the same question.
func (c Component) Key() string {
	if c.Name != "" {
		return c.Name
	}

	return c.Chart
}

// Deployed is what Deploy created, by component name. A component that
// declined to create anything is absent.
type Deployed map[string]pulumi.Resource

// Release returns a component's Helm release, for a caller that needs the
// release's own outputs rather than just something to depend on.
func (d Deployed) Release(name string) (*helm.Release, bool) {
	release, ok := d[name].(*helm.Release)

	return release, ok
}

// Deploy creates every component in dependency order.
func (r *Runner) Deploy(components Components) (Deployed, error) {
	ordered, err := order(components)
	if err != nil {
		return nil, err
	}

	deployed := make(Deployed, len(ordered))

	for _, component := range ordered {
		dependencies := resourcesFor(deployed, component.After)

		resource, createErr := r.create(component, dependencies)
		if createErr != nil {
			return nil, createErr
		}

		// A component may decline — an optional feature whose config is
		// unset. Absent from the map rather than nil in it, so a later
		// DependsOn cannot be handed a nil resource.
		if resource != nil {
			deployed[component.Key()] = resource
		}
	}

	return deployed, nil
}

func (r *Runner) create(component Component, dependencies []pulumi.Resource) (pulumi.Resource, error) {
	if component.Create != nil {
		return component.Create(r, dependencies)
	}

	var values pulumi.Map
	if component.Values != nil {
		values = component.Values(r)
	}

	return r.Release(ReleaseArgs{
		Chart:          component.Chart,
		Name:           component.Release,
		Values:         values,
		TimeoutSeconds: component.TimeoutSeconds,
		SkipCRDs:       component.SkipCRDs,
	}, DependsOn(dependencies)...)
}

// DependsOn turns a Create component's dependencies into resource options.
//
// Nothing when there are none, which is the point: `pulumi.DependsOn(nil)` is
// an option that says nothing, and the three places that needed this had
// three different answers to that — one guarded the call with a length check,
// one passed the empty option anyway, and one built the slice by hand. A
// component that declines to create anything is absent from Deployed, so an
// empty list is the ordinary case rather than an edge one.
//
// Exported because a layer's own Create function is handed the same slice and
// has to do the same thing with it.
func DependsOn(dependencies []pulumi.Resource) []pulumi.ResourceOption {
	if len(dependencies) == 0 {
		return nil
	}

	return []pulumi.ResourceOption{pulumi.DependsOn(dependencies)}
}

func resourcesFor(deployed Deployed, after []string) []pulumi.Resource {
	out := make([]pulumi.Resource, 0, len(after))

	for _, key := range after {
		if resource, ok := deployed[key]; ok {
			out = append(out, resource)
		}
	}

	return out
}

// visitState is where the topological sort has got to with one component.
//
// Named rather than the bare 0/1/2 this used to carry with a comment
// explaining them: a key found mid-visit is what closes a cycle, and that is
// the one case a reader has to get right.
type visitState int

const (
	unvisited visitState = iota
	visiting
	visitDone
)

// order returns the components sorted so that every After comes first.
//
// Deterministic on purpose: ties break by chart key rather than by map order,
// so two runs of the same contract produce the same sequence and a diff in the
// Pulumi preview means something actually changed.
func order(components Components) (Components, error) {
	byName := make(map[string]Component, len(components))
	keys := make([]string, 0, len(components))

	for _, component := range components {
		// Exactly one of the two, checked rather than assumed: a component
		// with both would silently deploy the chart and skip Create, and one
		// with neither would be a name in the order that creates nothing and
		// looks like it should.
		switch {
		case component.Chart == "" && component.Create == nil:
			return nil, fmt.Errorf("component %q has neither a chart nor a Create function", component.Key())
		case component.Chart != "" && component.Create != nil:
			return nil, fmt.Errorf("component %q has both a chart and a Create function", component.Key())
		case component.Key() == "":
			return nil, fmt.Errorf("a component built with Create has no Name")
		}

		key := component.Key()

		if _, seen := byName[key]; seen {
			return nil, fmt.Errorf("component %q appears twice in the set", key)
		}

		// Only a chart has a pin to check. The registry is what carries the
		// version, so a key it does not know is a chart with no pin.
		if component.Chart != "" {
			if _, err := charts.Get(component.Chart); err != nil {
				return nil, fmt.Errorf("component %q: %w", key, err)
			}
		}

		byName[key] = component
		keys = append(keys, key)
	}

	slices.Sort(keys)

	var (
		ordered Components
		state   = make(map[string]visitState, len(keys))
		visit   func(string, []string) error
	)

	visit = func(key string, path []string) error {
		switch state[key] {
		case visitDone:
			return nil
		case visiting:
			return fmt.Errorf("components form a dependency cycle: %v", append(path, key))
		case unvisited:
		}

		state[key] = visiting

		component := byName[key]

		// A sorted copy: the component's own After must not be reordered,
		// because a Components table is package-level data that outlives one
		// ordering pass.
		dependencies := slices.Sorted(slices.Values(component.After))

		for _, dependency := range dependencies {
			if _, ok := byName[dependency]; !ok {
				return fmt.Errorf(
					"component %q must follow %q, which is not in this set",
					key, dependency)
			}

			if err := visit(dependency, append(path, key)); err != nil {
				return err
			}
		}

		state[key] = visitDone

		ordered = append(ordered, component)

		return nil
	}

	for _, key := range keys {
		if err := visit(key, nil); err != nil {
			return nil, err
		}
	}

	return ordered, nil
}

// OrderForTest exposes the ordering to pkg/layer/layertest, which asserts that
// a component set can be ordered and that the order respects every After.
//
// Exported for that one caller rather than duplicating the algorithm in a
// test, which would then be able to disagree with the real one.
func OrderForTest(components Components) (Components, error) {
	return order(components)
}
