// Command token prints the Hetzner API token for a stack.
//
// It exists so a taskfile can put the token in the environment of a tool that
// reads it from there — the hcloud CLI — without restating how the token is
// found. That resolution is pkg/hetzner.Token: an exported HCLOUD_TOKEN
// first, then the encrypted stack config through Pulumi's own decryption.
//
// stdout, and only stdout: the caller is `export HCLOUD_TOKEN="$(…)"`, so the
// value never reaches an argument vector where `ps` would show it to every
// other user on the machine. The same reason cluster:token takes no token=
// argument.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/hetzner"
)

// timeout covers one `pulumi config get`, which decrypts through the service.
const timeout = 30 * time.Second

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: token <stack>")
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	token, err := hetzner.Token(ctx, args[0])
	if err != nil {
		return err
	}

	// No newline: the caller substitutes this into an environment variable,
	// and a trailing one would be trimmed by the shell here but not by every
	// caller. Token trims what it reads for the same reason.
	fmt.Print(token)

	return nil
}
