// Command secrets prints a stack's Talos secrets bundle.
//
// The bundle is the cluster's root of trust: the CA keys every node and
// client certificate descends from, the bootstrap tokens, and the secretbox
// key that encrypts Kubernetes Secrets inside etcd. It lives in exactly one
// place — Pulumi's state — and `Protect` keeps a destroy from taking it, which
// is not the same as having a second copy.
//
// It is also the half that makes an etcd snapshot mean anything:
// `talosctl bootstrap --recover-from` accepts a snapshot only against the same
// secrets, and the `secrets` resource inside a snapshot is ciphertext under
// the secretbox key. Stored apart from each other, neither half is a backup.
//
// stdout, and only stdout — the same discipline as tools/token, and for the
// same reason: a value in an argument vector is visible to every other user on
// the machine through `ps`. It refuses a terminal outright, because the one
// thing worse than no copy of a certificate authority is one sitting in
// scrollback.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/secretout"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/talossecrets"
)

// timeout covers one `pulumi stack export`, which decrypts through the
// backend.
const timeout = 60 * time.Second

func main() {
	if err := run(context.Background(), os.Args[1:], secretout.IsTerminal()); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, terminal bool) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: secrets <stack>")
	}

	if terminal {
		return secretout.Refuse("the cluster's certificate authority",
			"task cluster:secrets:export stack="+args[0],
			"hetzner/"+args[0]+"/talos-secrets")
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	bundle, err := talossecrets.Bundle(ctx, args[0])
	if err != nil {
		return err
	}

	_, err = os.Stdout.Write(append(bundle, '\n'))

	return err
}
