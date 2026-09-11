package layer

import (
	"fmt"
	"sort"

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
// add an entry to that layer's Components and give it a values function; to
// make it wait for another, name that other component's chart key in After.
// pkg/layer/layertest.Check then holds it to the invariants — pinned chart, no
// cycle, workloads declared — without the layer writing a test for each.
type Components []Component

// Component is one deployable piece of a layer.
type Component struct {
	// Chart is a key into pkg/charts. It is also this component's name, the
	// one other components use in After.
	Chart string

	// Release overrides the Helm release name. Empty uses the chart key.
	Release string

	// TimeoutSeconds overrides the default for a chart that is genuinely slow.
	TimeoutSeconds int

	// After names the chart keys this component must follow. It becomes a
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

// Deploy creates every component in dependency order and returns the releases
// by chart key, so a caller can depend on one from outside the set.
func (r *Runner) Deploy(components Components) (map[string]*helm.Release, error) {
	ordered, err := order(components)
	if err != nil {
		return nil, err
	}

	released := make(map[string]*helm.Release, len(ordered))

	for _, component := range ordered {
		var values pulumi.Map
		if component.Values != nil {
			values = component.Values(r)
		}

		opts := make([]pulumi.ResourceOption, 0, 1)

		if dependencies := resourcesFor(released, component.After); len(dependencies) > 0 {
			opts = append(opts, pulumi.DependsOn(dependencies))
		}

		release, releaseErr := r.Release(r.Ctx, ReleaseArgs{
			Chart:          component.Chart,
			Name:           component.Release,
			Values:         values,
			TimeoutSeconds: component.TimeoutSeconds,
			SkipCRDs:       component.SkipCRDs,
		}, opts...)
		if releaseErr != nil {
			return nil, releaseErr
		}

		released[component.Chart] = release
	}

	return released, nil
}

func resourcesFor(released map[string]*helm.Release, after []string) []pulumi.Resource {
	out := make([]pulumi.Resource, 0, len(after))

	for _, key := range after {
		if release, ok := released[key]; ok {
			out = append(out, release)
		}
	}

	return out
}

// order returns the components sorted so that every After comes first.
//
// Deterministic on purpose: ties break by chart key rather than by map order,
// so two runs of the same contract produce the same sequence and a diff in the
// Pulumi preview means something actually changed.
func order(components Components) (Components, error) {
	byChart := make(map[string]Component, len(components))
	keys := make([]string, 0, len(components))

	for _, component := range components {
		if component.Chart == "" {
			return nil, fmt.Errorf("a component has no chart key")
		}

		if _, seen := byChart[component.Chart]; seen {
			return nil, fmt.Errorf("chart %q appears twice in the set", component.Chart)
		}

		if _, err := charts.Get(component.Chart); err != nil {
			return nil, fmt.Errorf("component %q: %w", component.Chart, err)
		}

		byChart[component.Chart] = component
		keys = append(keys, component.Chart)
	}

	sort.Strings(keys)

	var (
		ordered Components
		state   = make(map[string]int, len(keys))
		visit   func(string, []string) error
	)

	// 1 is "being visited", 2 is "done". A key found at 1 closes a cycle.
	visit = func(key string, path []string) error {
		switch state[key] {
		case 2:
			return nil
		case 1:
			return fmt.Errorf("components form a dependency cycle: %v", append(path, key))
		}

		state[key] = 1

		component := byChart[key]

		dependencies := make([]string, len(component.After))
		copy(dependencies, component.After)
		sort.Strings(dependencies)

		for _, dependency := range dependencies {
			if _, ok := byChart[dependency]; !ok {
				return fmt.Errorf(
					"component %q must follow %q, which is not in this set",
					key, dependency)
			}

			if err := visit(dependency, append(path, key)); err != nil {
				return err
			}
		}

		state[key] = 2

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
