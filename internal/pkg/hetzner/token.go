package hetzner

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
)

const (
	// TokenEnv is the variable an operator may export to override the stack's.
	TokenEnv = "HCLOUD_TOKEN"

	// TokenConfigKey is where the encrypted token lives in the stack.
	TokenConfigKey = "hcloud:token"

	// ClusterDir is the Pulumi project that holds it — the one project that
	// talks to the Hetzner API, and so the one stack with a token.
	ClusterDir = "infra/cluster"
)

// Token resolves the Hetzner API token: an exported one first, then the
// encrypted stack config.
//
// An exported token wins so a shell that already has one — CI, or a token for
// another project — keeps working. Otherwise it comes out of Pulumi's own
// ciphertext in the gitignored Pulumi.<stack>.yaml, which is why no plaintext
// file has to exist anywhere.
//
// Here rather than in one tool, because two tools need it and the alternative
// is two copies of the same incantation with two versions of the remedy. Not
// resolved in a taskfile `env:` stanza either: Task evaluates those BEFORE
// preconditions, so the precondition holding the remedy could never print —
// this repository has made that mistake once already, in cluster:image:bake.
func Token(ctx context.Context, stack string) (string, error) {
	// Trimmed, like the value read from the stack below. A token pasted with
	// a trailing newline authenticates nothing, and the API answers it with a
	// 401 that reads like a wrong token rather than a badly copied one.
	if token := strings.TrimSpace(os.Getenv(TokenEnv)); token != "" {
		return token, nil
	}

	if stack == "" {
		return "", fmt.Errorf("no %s exported and no stack to read one from", TokenEnv)
	}

	// The automation API's workspace rather than `exec.Command("pulumi", …)`.
	//
	// It decrypts the secret through the same code path the CLI uses, returns
	// a typed ConfigValue instead of stdout to trim, and leaves no argument
	// vector for a reader to audit — the #nosec this function used to carry
	// was explaining that a stack name cannot reach a shell.
	//
	// A workspace, not a selected stack: auto.SelectStackLocalSource would
	// run `pulumi stack select` as a side effect and repoint the operator's
	// own workspace. GetConfig takes the stack name as an argument and
	// changes nothing.
	workspace, err := auto.NewLocalWorkspace(ctx, auto.WorkDir(ClusterDir))
	if err != nil {
		return "", fmt.Errorf("open %s as a pulumi workspace: %w", ClusterDir, err)
	}

	value, err := workspace.GetConfig(ctx, stack, TokenConfigKey)
	if err != nil {
		return "", fmt.Errorf(
			"%w\nno Hetzner token for stack %s. Set it once, encrypted, in the stack:\n\n"+
				"  pulumi -C %s -s %s config set --secret %s <token>\n\n"+
				"It is then stored as ciphertext in Pulumi.%s.yaml and every task reads it\n"+
				"from there. Exporting %s also works and takes priority",
			err, stack, ClusterDir, stack, TokenConfigKey, stack, TokenEnv)
	}

	token := strings.TrimSpace(value.Value)
	if token == "" {
		return "", fmt.Errorf("stack %s has an empty %s", stack, TokenConfigKey)
	}

	return token, nil
}
