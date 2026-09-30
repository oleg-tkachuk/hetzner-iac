package main

import (
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
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

// TestPasswordSpecialCharacters_SurviveAnInteractiveShell is the half the list
// above missed: characters that are harmless in a script and change the value
// typed at a prompt.
//
// `!` is history expansion in interactive bash and zsh, inside double quotes
// too, so `export RESTIC_PASSWORD="…!x…"` substitutes a past command into the
// key. The rest are expansions of an unquoted word — globs, a subshell, brace
// expansion, a comment, tilde and zsh's `=command` — and a key pasted
// unquoted becomes a different key or no command at all.
func TestPasswordSpecialCharacters_SurviveAnInteractiveShell(t *testing.T) {
	t.Parallel()

	for _, dangerous := range []string{
		"!",                // history expansion, even between double quotes
		"*", "?", "[", "]", // globs
		"(", ")", // a subshell, or extglob's @(…)
		"{", "}", // brace expansion
		"#", // a comment at the start of a word
		"~", // tilde expansion, after `=` and `:` in an assignment too
		"^", // zsh's extended glob, and bash's ^old^new
		"=", // zsh's =command expansion at the start of a word
	} {
		assert.NotContains(t, PasswordSpecialCharacters, dangerous,
			"%q is in the special set, and it changes a password typed at an interactive prompt",
			dangerous)
	}
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

// keyRegistration is what the mock monitor saw when the restic key was
// registered: the inputs sent, and the two options that keep the key.
type keyRegistration struct {
	mu        sync.Mutex
	inputs    []string
	protected bool
	ignored   []string
}

func (k *keyRegistration) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	if args.Name == repositoryKeyName {
		for key := range args.Inputs {
			k.inputs = append(k.inputs, string(key))
		}

		if rpc := args.RegisterRPC; rpc != nil {
			k.protected = rpc.GetProtect()
			k.ignored = rpc.GetIgnoreChanges()
		}
	}

	return args.Name + "-id", args.Inputs, nil
}

func (*keyRegistration) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return args.Args, nil
}

func registerRepositoryKey(t *testing.T) *keyRegistration {
	t.Helper()

	registered := &keyRegistration{}

	require.NoError(t, pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := newRepositoryKey(ctx)

		return err
	}, pulumi.WithMocks("backup", "dev", registered)))

	return registered
}

// TestRepositoryKey_IsProtected keeps a replace or a delete of the key from
// going through: either one discards the only thing that decrypts the
// snapshots already on the box.
func TestRepositoryKey_IsProtected(t *testing.T) {
	t.Parallel()

	assert.True(t, registerRepositoryKey(t).protected,
		"the restic key is not pulumi.Protect-ed, so a rename or a replace drops it and every "+
			"snapshot on the box becomes unreadable")
}

// TestRepositoryKey_IgnoresEveryInputItIsGiven is the bug: the key shares
// passwordArgs with the box passwords, so changing that alphabet for Hetzner
// replaced it too. Held against the inputs actually sent, so a field added to
// passwordArgs later fails here rather than rotating the key.
func TestRepositoryKey_IgnoresEveryInputItIsGiven(t *testing.T) {
	t.Parallel()

	registered := registerRepositoryKey(t)

	require.NotEmpty(t, registered.inputs, "the key was registered with no inputs, so this proved nothing")
	assert.Subset(t, registered.ignored, registered.inputs,
		"an input of the restic key is not ignored, so changing it draws a new key")
}
