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
// registered: the generator, and the stash that keeps its first value.
type keyRegistration struct {
	mu sync.Mutex

	generatorProtected bool
	generatorIgnored   []string
	generatorInputs    []string

	stashed         bool
	stashProtected  bool
	stashSecrets    []string
	stashInputIsKey bool
}

// generatedKey is the value the mock generator produces.
const generatedKey = "generated-key"

func (k *keyRegistration) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	rpc := args.RegisterRPC

	switch args.Name {
	case repositoryKeyName:
		for key := range args.Inputs {
			k.generatorInputs = append(k.generatorInputs, string(key))
		}

		if rpc != nil {
			k.generatorProtected = rpc.GetProtect()
			k.generatorIgnored = rpc.GetIgnoreChanges()
		}

		outputs := args.Inputs.Copy()
		outputs["result"] = resource.MakeSecret(resource.NewStringProperty(generatedKey))

		return args.Name + "-id", outputs, nil
	case repositoryKeyStash:
		k.stashed = args.TypeToken == "pulumi:index:Stash"

		if rpc != nil {
			k.stashProtected = rpc.GetProtect()
			k.stashSecrets = rpc.GetAdditionalSecretOutputs()
		}

		input := args.Inputs[stashInput]
		if input.IsSecret() {
			input = input.SecretValue().Element
		}

		k.stashInputIsKey = input.IsString() && input.StringValue() == generatedKey

		// What the engine does on create: the output is the input.
		return args.Name + "-id", resource.PropertyMap{
			stashInput:  args.Inputs[stashInput],
			stashOutput: args.Inputs[stashInput],
		}, nil
	}

	return args.Name + "-id", args.Inputs, nil
}

func (*keyRegistration) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return args.Args, nil
}

func registerRepositoryKey(t *testing.T) (*keyRegistration, string) {
	t.Helper()

	registered := &keyRegistration{}
	resolved := make(chan string, 1)

	require.NoError(t, pulumi.RunErr(func(ctx *pulumi.Context) error {
		key, err := newRepositoryKey(ctx)
		if err != nil {
			return err
		}

		key.ApplyT(func(value string) string {
			resolved <- value

			return value
		})

		return nil
	}, pulumi.WithMocks("backup", "dev", registered)))

	return registered, <-resolved
}

// TestRepositoryKey_IsTheStashedValue: the key is what the stash kept, so a
// replaced generator — a change to passwordArgs, which the box passwords
// share — leaves it where it was.
func TestRepositoryKey_IsTheStashedValue(t *testing.T) {
	t.Parallel()

	registered, key := registerRepositoryKey(t)

	require.True(t, registered.stashed, "the key is not kept in a pulumi:index:Stash")
	assert.True(t, registered.stashInputIsKey, "the stash is not given the generator's result")
	assert.Equal(t, generatedKey, key, "the key is not read from the stash's output")
}

// TestRepositoryKey_StashIsProtectedAndSecret keeps a rename or a destroy of
// the stash from discarding the key, and the key out of state in plaintext.
func TestRepositoryKey_StashIsProtectedAndSecret(t *testing.T) {
	t.Parallel()

	registered, _ := registerRepositoryKey(t)

	assert.True(t, registered.stashProtected,
		"the stash is not pulumi.Protect-ed, so a rename drops the key and every snapshot "+
			"on the box becomes unreadable")
	assert.ElementsMatch(t, []string{stashInput, stashOutput}, registered.stashSecrets,
		"both of the stash's properties hold the key and must be secret in state")
}

// TestRepositoryKey_GeneratorIsNotProtected: the stash is what is protected.
// A protected generator would turn a harmless replacement into a refused
// preview.
func TestRepositoryKey_GeneratorIsNotProtected(t *testing.T) {
	t.Parallel()

	registered, _ := registerRepositoryKey(t)

	assert.False(t, registered.generatorProtected)
}

// TestRepositoryKey_GeneratorIgnoresEveryInput: the stash adopts whatever the
// generator holds when it is created, and existing state holds a key drawn
// with an older alphabet. Measured on dev: without the ignore, the change that
// added the stash replaced the generator in the same apply.
func TestRepositoryKey_GeneratorIgnoresEveryInput(t *testing.T) {
	t.Parallel()

	registered, _ := registerRepositoryKey(t)

	require.NotEmpty(t, registered.generatorInputs, "the generator was registered with no inputs, so this proved nothing")
	assert.Subset(t, registered.generatorIgnored, registered.generatorInputs,
		"an input of the generator is not ignored, so the stash would adopt a new key")
}
