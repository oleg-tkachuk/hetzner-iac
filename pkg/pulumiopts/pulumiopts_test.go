package pulumiopts_test

import (
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/pulumiopts"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// base returns an options slice with SPARE CAPACITY, which is the condition
// that makes append dangerous. A slice at capacity reallocates on append and
// the bug does not appear — which is why it hides in review and in tests that
// build their fixtures the obvious way.
func base(t *testing.T) []pulumi.ResourceOption {
	t.Helper()

	opts := make([]pulumi.ResourceOption, 0, 4)
	opts = append(opts, pulumi.RetainOnDelete(false))

	require.Len(t, opts, 1)
	require.GreaterOrEqual(t, cap(opts), 3, "the fixture must have spare capacity or it proves nothing")

	return opts
}

// TestWith_DoesNotAliasTheSliceItWasGiven is the whole point of the package.
//
// Two children built from one parent, the way a component builds options for
// two resources. With `append` they share a backing array and the second write
// lands in the first child's slot; the assertion here is that they do not.
func TestWith_DoesNotAliasTheSliceItWasGiven(t *testing.T) {
	t.Parallel()

	parent := base(t)

	first := pulumi.Timeouts(&pulumi.CustomTimeouts{Create: "1m"})
	second := pulumi.Timeouts(&pulumi.CustomTimeouts{Create: "2m"})

	childA := pulumiopts.With(parent, first)
	childB := pulumiopts.With(parent, second)

	require.Len(t, childA, 2)
	require.Len(t, childB, 2)

	// Addresses, not values: a ResourceOption is an opaque interface value and
	// two of them cannot be compared, while the property under test is about
	// the BACKING ARRAY. Separate slots is exactly "the second child did not
	// overwrite the first child's option".
	assert.NotSame(t, &childA[1], &childB[1],
		"the children share a slot, so whichever option was added second replaced the first")
	assert.NotSame(t, &parent[0], &childA[0], "With handed back the parent's own array")

	// And the parent is untouched, so a caller can keep using it.
	assert.Len(t, parent, 1)
}

// TestAppend_AliasesTheSliceItWasGiven records the behaviour this package
// exists to avoid, so the reason is a failing assertion away rather than a
// claim in a comment.
//
// It asserts the BUG on purpose: run the same two lines with `append` and the
// first child ends up holding the second child's option. If a future Go
// changes this, the test fails and the package's reason for existing needs
// re-reading.
func TestAppend_AliasesTheSliceItWasGiven(t *testing.T) {
	t.Parallel()

	parent := base(t)

	first := pulumi.Timeouts(&pulumi.CustomTimeouts{Create: "1m"})
	second := pulumi.Timeouts(&pulumi.CustomTimeouts{Create: "2m"})

	childA := append(parent, first)  //nolint:gocritic // the aliasing is the subject
	childB := append(parent, second) //nolint:gocritic // the aliasing is the subject

	require.Len(t, childA, 2)
	require.Len(t, childB, 2)

	assert.Same(t, &childA[1], &childB[1],
		"append no longer aliases a slice with spare capacity; pkg/pulumiopts may be unnecessary")
	assert.Same(t, &parent[0], &childA[0])
}

// TestWith_NoExtraIsACopy keeps the degenerate call honest.
//
// A component that adds nothing still must not hand its parent's slice
// onward: the next caller would append into it and be back in the same trap,
// one level further from the code that caused it.
func TestWith_NoExtraIsACopy(t *testing.T) {
	t.Parallel()

	parent := base(t)
	child := pulumiopts.With(parent)

	require.Len(t, child, len(parent))
	require.NotEmpty(t, child)

	// Separate arrays is the whole guarantee: a later append through the child
	// cannot reach the parent, because there is nothing shared to reach.
	assert.NotSame(t, &parent[0], &child[0], "With returned the parent's own array")
}

// TestWith_NilBase is the first call in a chain, where there are no options
// yet. It must not be a special case at the call site.
func TestWith_NilBase(t *testing.T) {
	t.Parallel()

	only := pulumi.RetainOnDelete(true)

	got := pulumiopts.With(nil, only)

	assert.Len(t, got, 1)
}
