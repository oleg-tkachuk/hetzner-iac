// Package pulumiopts combines resource options without aliasing the slice it
// was given.
//
// One function, and it exists because `append` is the wrong tool for this and
// looks like the right one:
//
//	child := append(opts, pulumi.DependsOn(...))
//
// When opts has spare capacity, that writes into the CALLER's backing array.
// Two such lines write the same slot, so whichever option is added second
// silently replaces the first — and the resource that was given the first
// gets the second's dependencies. Nothing reports it: both slices are the
// right length, every option is a valid option, and the only symptom is a
// creation order that is wrong under conditions nobody chose.
//
// The repository's own comment on internal/pkg/layer.Runner.With names the same
// failure: "appending to r.Options directly would let one component's
// DependsOn leak into every component created after it."
package pulumiopts

import "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

// With returns base plus extra as a new slice, never touching base.
//
// The allocation is the point, not a cost to optimise away: a shared backing
// array is exactly the thing being avoided. It is also once per resource, next
// to a network call.
func With(base []pulumi.ResourceOption, extra ...pulumi.ResourceOption) []pulumi.ResourceOption {
	out := make([]pulumi.ResourceOption, 0, len(base)+len(extra))
	out = append(out, base...)
	out = append(out, extra...)

	return out
}
