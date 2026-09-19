package main

import (
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/internals"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPasswordArgs_SatisfyHetznersPolicy is the test the first live apply of
// this layer should not have had to be.
//
// Hetzner answered 422 invalid_input:
//
//	The password must contain at least one upper case letter, one lower case
//	letter, one number, and a special character
//
// The args said `Special: false` and nothing else, so two resources errored
// after four had been created. Length is not the constraint and never was —
// the four minimums are, and RandomPassword satisfies them only when asked.
func TestPasswordArgs_SatisfyHetznersPolicy(t *testing.T) {
	t.Parallel()

	args := passwordArgs()

	special, err := internals.UnsafeAwaitOutput(t.Context(), args.Special.ToBoolPtrOutput())
	require.NoError(t, err)
	require.NotNil(t, special.Value)

	enabled, ok := special.Value.(*bool)
	require.True(t, ok)
	require.NotNil(t, enabled)

	assert.True(t, *enabled,
		"Special is off, so the generated password holds no special character and Hetzner "+
			"refuses it with 422 invalid_input")

	// Each class explicitly, because Hetzner's message names four and a loop
	// over three would still pass.
	for name, minimum := range map[string]pulumi.IntPtrInput{
		"MinUpper":   args.MinUpper,
		"MinLower":   args.MinLower,
		"MinNumeric": args.MinNumeric,
		"MinSpecial": args.MinSpecial,
	} {
		require.NotNil(t, minimum, "%s is unset, so that character class is not guaranteed", name)

		resolved, awaitErr := internals.UnsafeAwaitOutput(t.Context(), minimum.ToIntPtrOutput())
		require.NoError(t, awaitErr, name)

		count, ok := resolved.Value.(*int)
		require.True(t, ok, name)
		require.NotNil(t, count, name)

		assert.Positive(t, *count,
			"%s is %d, so a password with none of that class satisfies the args and Hetzner "+
				"refuses it", name, *count)
	}
}

// TestPasswordSpecialCharacters_SurviveAShell keeps the override narrow.
//
// Every one of these passwords reaches a process through an environment
// variable, where any byte is safe. The restic key is different: the operator
// copies it out of the stack by hand, and a password holding a quote, a
// backslash, a backtick or a dollar breaks the moment it is pasted.
func TestPasswordSpecialCharacters_SurviveAShell(t *testing.T) {
	t.Parallel()

	require.NotEmpty(t, PasswordSpecialCharacters)

	for _, dangerous := range []string{`"`, `'`, "`", `\`, `$`, `;`, `|`, `<`, `>`, `&`, " ", "\t"} {
		assert.NotContains(t, PasswordSpecialCharacters, dangerous,
			"%q is in the special set, and a password carrying it cannot be pasted into a shell",
			dangerous)
	}

	// And it has to contain something, or MinSpecial cannot be satisfied.
	assert.GreaterOrEqual(t, len(strings.TrimSpace(PasswordSpecialCharacters)), 4)
}

// TestPasswordSpecialCharacters_AreAllAcceptedByHetzner is the other half of
// the constraint, and the half that was missing.
//
// The set was narrowed for the shell and never checked against the API, so it
// held `[` and `]` — which Hetzner refuses. That surfaced as a 422 at APPLY
// time, with five resources created and the Storage Box and its subaccount
// errored:
//
//	invalid input in field password (invalid_input)
//	The password can only contain these characters: a-z A-Z Ä Ö Ü ä ö ü ß
//	0-9 ^ ° ! § $ % / ( ) = ? + # - . , ; : ~ * @ { } _ &
//
// Nothing offline said a word. This does, and it costs one loop.
func TestPasswordSpecialCharacters_AreAllAcceptedByHetzner(t *testing.T) {
	t.Parallel()

	for _, character := range PasswordSpecialCharacters {
		assert.Contains(t, HetznerPasswordSpecialCharacters, string(character),
			"%q is in the alphabet passwords are drawn from and Hetzner's Storage Box API "+
				"rejects it: the apply fails after the passwords exist",
			string(character))
	}
}

// TestPasswordSpecialCharacters_RejectTheCharactersThatFailedTheApply pins the
// two that did it, so the regression cannot come back by someone widening the
// set to something that merely looks safe.
func TestPasswordSpecialCharacters_RejectTheCharactersThatFailedTheApply(t *testing.T) {
	t.Parallel()

	for _, refused := range []string{"[", "]"} {
		assert.NotContains(t, PasswordSpecialCharacters, refused,
			"%q is not in Hetzner's accepted set", refused)
		assert.NotContains(t, HetznerPasswordSpecialCharacters, refused,
			"%q is recorded as accepted by Hetzner, and the API says otherwise", refused)
	}
}
