// Command token prints the Hetzner API token for a stack.
//
// It exists so a taskfile can put the token in the environment of a tool that
// reads it from there — the hcloud CLI — without restating how the token is
// found. That resolution is internal/pkg/hcloudtoken.Token: an exported HCLOUD_TOKEN
// first, then the encrypted stack config through Pulumi's own decryption.
//
// stdout, and only stdout: the caller is `export HCLOUD_TOKEN="$(…)"`, so the
// value never reaches an argument vector where `ps` would show it to every
// other user on the machine. The same reason cluster:token takes no token=
// argument — and the reason it refuses a terminal, as tools/secrets and
// tools/recoverykit do: a token on a screen stays in scrollback.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/hcloudtoken"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/secretout"
)

// timeout covers one `pulumi config get`, which decrypts through the service.
const timeout = 30 * time.Second

func main() {
	if err := run(os.Args[1:], secretout.IsTerminal()); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, terminal bool) error {
	if len(args) != 1 || args[0] == "" {
		// The remedy, not just the shape. This runs inside tasks that take
		// stack= and have no default, and an operator who forgot it sees this
		// message rather than the task's own: the shared hcloud module
		// resolves the token through here before any precondition of its own.
		return fmt.Errorf("usage: token <stack>\n\n" +
			"Every task here takes a stack and there is no default:\n\n" +
			"    task <task> stack=dev\n\n" +
			"The stack names both the Pulumi stack and infra/cluster/cluster.<stack>.yaml")
	}

	// Refused before the token is even resolved. It is a key to the whole
	// Hetzner project, and on a screen it outlives the session in scrollback —
	// the rule tools/secrets and tools/recoverykit keep through secretout. Not
	// secretout.Refuse's remedy, though: this value belongs in a command's
	// environment, not in a password store, so the remedy is that use.
	if terminal {
		return fmt.Errorf("%w: the Hetzner API token is for a command's environment, for example\n\n"+
			"  export HCLOUD_TOKEN=\"$(go run ./tools/token %s)\"", secretout.ErrTerminal, args[0])
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	token, err := hcloudtoken.Token(ctx, args[0])
	if err != nil {
		return err
	}

	// No newline: the caller substitutes this into an environment variable,
	// and a trailing one would be trimmed by the shell here but not by every
	// caller. Token trims what it reads for the same reason.
	fmt.Print(token)

	return nil
}
