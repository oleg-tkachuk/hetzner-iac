package layer

import (
	"errors"
	"fmt"
	"slices"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/charts"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/values"

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
// that other component in After. internal/pkg/layer/layertest.Check then holds the set
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
//
// Three fields went from the API after v4.14.0, all of them about values:
// Component.ValuesYAML and Component.Values, replaced by StaticValues and
// ValuesFrom below, and ReleaseArgs.Values, which nothing used. A caller
// holding a component literal with ValuesYAML moves the closure's body into
// ValuesFrom and deletes the chart key it was repeating.
type Component struct {
	// Name is what other components use in After. Empty takes Chart, which is
	// what a release wants; a Create component has to set it.
	Name string

	// Chart is a key into internal/pkg/charts. Set it for a Helm release, and leave
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
	Create CreateFunc

	// Release overrides the Helm release name. Empty uses the chart key.
	Release string

	// TimeoutSeconds overrides the default for a chart that is genuinely slow.
	TimeoutSeconds int

	// After names the components this one must follow, by Name. It becomes a
	// DependsOn, so it is a fact the engine holds rather than a convention the
	// reader has to keep.
	After []string

	// StaticValues is this chart's template data when nothing has to be
	// resolved at run time. Nil is a legitimate value: four of the templates
	// take no data and exist to set resource limits the chart's own defaults
	// leave off.
	//
	// The chart key is not repeated here. It is Chart, and the template is
	// named after it — which is the point of the field: it used to be a
	// closure calling values.Static("cert-manager", nil) beside
	// Chart: "cert-manager", so the key was spelled twice per component with
	// nothing comparing the two.
	StaticValues any

	// ValuesFrom resolves the cluster tier's outputs, or stack config, into
	// the template's data. For a template that cannot be rendered until a
	// StackReference has resolved.
	//
	// Set this or StaticValues, never both; order refuses a component that
	// sets both rather than picking one silently.
	ValuesFrom func(*Runner) pulumi.Output

	// Group parents this component's resources under a component resource, so
	// the state says which former layer they belong to. Empty leaves them
	// where they are, directly under the stack.
	//
	// Grouping is not ordering: see Group. A layer that was never merged with
	// another needs none.
	Group Group

	// SkipCRDs leaves custom resource definitions alone.
	SkipCRDs bool

	// When decides whether this component is created at all, from stack
	// configuration. Nil means always.
	//
	// A Create component already expresses this by returning (nil, nil) — the
	// ClusterIssuer does, when acmeEmail is unset. A CHART component could
	// not: everything about installing one is this package's job, so declining
	// meant writing a Create that repeated values.Static and Runner.Release to
	// get at one `if`. KEDA is the first optional chart here.
	//
	// The entry stays in the set either way — still enumerated, still ordered,
	// still paired with its workloads by layertest — rather than the decision
	// being hidden behind an `if` around the literal, where nothing can see
	// it.
	//
	// It returns an error, not just a bool, because the answer comes from a
	// string an operator typed: `kedaEnabled: yes` is not a boolean, and
	// reading it as false gives a cluster with no KEDA and an operator who
	// believes otherwise. layers/20-network-policy makes the same argument
	// about its own switch.
	When func(*Runner) (bool, error)
}

// Key is the name other components refer to this one by: Name, or the chart
// key when Name is empty. Exported because internal/pkg/layer/layertest asserts the
// order and has to ask the same question.
func (c Component) Key() string {
	if c.Name != "" {
		return c.Name
	}

	return c.Chart
}

// CreateFunc is what a component that is not a chart does.
//
// Named because there are now two ways to write one: a plain function, and a
// function that RETURNS one — layers/30-cluster-services closes over the Hetzner
// provider that way, since the provider exists only once the cluster tier's
// token has been read. An unnamed signature written at both is the same
// contract twice.
type CreateFunc func(*Runner, []pulumi.Resource) (pulumi.Resource, error)

// Deployed is what Deploy created, by component name. A component that
// declined to create anything is absent.
type Deployed map[string]pulumi.Resource

// Release returns a component's Helm release, for a caller that needs the
// release's own outputs rather than just something to depend on.
func (d Deployed) Release(name string) (*helm.Release, bool) {
	release, ok := d[name].(*helm.Release)

	return release, ok
}

// MustRelease is Release for the caller that cannot continue without it —
// a layer exporting the release's readiness, which is every caller so far.
//
// The two that existed wrote the same four lines with their own wording of
// the error. A component absent from Deployed either declined to create
// anything or is not in the set at all, and both are programming errors in
// the layer rather than states to handle.
func (d Deployed) MustRelease(name string) (*helm.Release, error) {
	release, ok := d.Release(name)
	if !ok {
		return nil, fmt.Errorf("%s was not deployed as a Helm release", name)
	}

	return release, nil
}

// Deploy creates every component in dependency order.
func (r *Runner) Deploy(components Components) (Deployed, error) {
	ordered, err := order(components)
	if err != nil {
		return nil, err
	}

	deployed := make(Deployed, len(ordered))
	parents := make(map[Group]*groupParent)

	for _, component := range ordered {
		dependencies := resourcesFor(deployed, component.After)

		scoped := r

		if !component.Group.Empty() {
			parent, groupErr := r.parentFor(parents, component.Group)
			if groupErr != nil {
				return nil, groupErr
			}

			scoped = r.inGroup(parent)
		}

		resource, createErr := scoped.create(component, dependencies)
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

	// After the children, because that is when a component resource's outputs
	// are registered. What each group held is the one thing the state can say
	// about a grouping node, and it is what answers "what would --target on
	// this group have taken" without re-reading the program.
	for group, keys := range groupsOf(ordered) {
		if err := r.Ctx.RegisterResourceOutputs(parents[group], pulumi.Map{
			"components": pulumi.ToStringArray(keys),
		}); err != nil {
			return nil, fmt.Errorf("register outputs for group %s: %w", group, err)
		}
	}

	return deployed, nil
}

func (r *Runner) create(component Component, dependencies []pulumi.Resource) (pulumi.Resource, error) {
	if component.When != nil {
		wanted, err := component.When(r)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", component.Key(), err)
		}

		if !wanted {
			return nil, nil
		}
	}

	if component.Create != nil {
		return component.Create(r, dependencies)
	}

	rendered, err := render(r, component)
	if err != nil {
		return nil, err
	}

	return r.Release(ReleaseArgs{
		Chart:          component.Chart,
		Name:           component.Release,
		ValuesYAML:     rendered,
		TimeoutSeconds: component.TimeoutSeconds,
		SkipCRDs:       component.SkipCRDs,
	}, DependsOn(dependencies)...)
}

// render turns a chart component's values into the file Helm reads.
//
// Unconditionally, and that is the invariant worth having: every chart in
// internal/pkg/charts has exactly one template in internal/pkg/values — eight and eight — so a
// chart component with no values is a chart installing on its own defaults,
// which is the outcome the templates exist to prevent. It used to be possible
// to express by leaving one field nil.
func render(r *Runner, component Component) (pulumi.AssetOrArchiveArrayInput, error) {
	if component.ValuesFrom != nil {
		return values.Asset(component.Chart, component.ValuesFrom(r)), nil
	}

	rendered, err := values.Static(component.Chart, component.StaticValues)
	if err != nil {
		return nil, fmt.Errorf("values for %s: %w", component.Key(), err)
	}

	return rendered, nil
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

// index checks every component and returns the set keyed by name.
//
// Separate from order because the two answer different questions: this one is
// about whether a component set is well formed at all, and order is about the
// sequence it produces. Together they were one function doing both, where the
// sort was the part a reader had come to read.
func index(components Components) (map[string]Component, error) {
	byName := make(map[string]Component, len(components))

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
			return nil, errors.New("a component built with Create has no Name")
		case component.StaticValues != nil && component.ValuesFrom != nil:
			return nil, fmt.Errorf(
				"component %q sets both StaticValues and ValuesFrom; one template takes one of them",
				component.Key())
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
	}

	return byName, nil
}

// order returns the components sorted so that every After comes first.
//
// Deterministic on purpose: ties break by chart key rather than by map order,
// so two runs of the same contract produce the same sequence and a diff in the
// Pulumi preview means something actually changed.
func order(components Components) (Components, error) {
	byName, err := index(components)
	if err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(byName))
	for key := range byName {
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

			// Refused, because a group is meant to be independently
			// appliable. Two former layers in one project stay separable only
			// while nothing in one waits for anything in the other: an After
			// across the boundary becomes a DependsOn, and then
			// `destroy --target` on one group refuses without
			// --target-dependents and takes the other group's resource with it
			// when given one. If the dependency is real, the two belong in one
			// group and the merge was the wrong shape.
			if other := byName[dependency].Group; other != component.Group {
				return fmt.Errorf(
					"component %q in group %s must follow %q in group %s: a dependency "+
						"across groups makes neither group independently appliable",
					key, component.Group, dependency, other)
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

// OrderForTest exposes the ordering to internal/pkg/layer/layertest, which asserts that
// a component set can be ordered and that the order respects every After.
//
// Exported for that one caller rather than duplicating the algorithm in a
// test, which would then be able to disagree with the real one.
func OrderForTest(components Components) (Components, error) {
	return order(components)
}
