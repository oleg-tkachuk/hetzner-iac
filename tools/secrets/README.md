# secrets

Prints a stack's Talos secrets bundle on stdout.

```bash
go run ./tools/secrets <stack>
```

The bundle is the cluster's root of trust: the CA keys every node and client
certificate descends from, the bootstrap tokens, and the secretbox key that
encrypts Kubernetes Secrets inside etcd. It lives in exactly one place —
Pulumi's state — where `Protect` keeps a destroy from taking it, which is not
the same as having a second copy.

It is also the half that makes an etcd snapshot mean anything. `talosctl
bootstrap --recover-from` accepts a snapshot only against the same secrets,
and the `secrets` resource inside a snapshot is ciphertext under the secretbox
key. Stored apart from each other, neither half is a backup.

stdout and only stdout, the same discipline as [token](../token): a value in an
argument vector is visible to every other user on the machine through `ps`. It
**refuses a terminal** outright — the one thing worse than no copy of a
certificate authority is one sitting in scrollback.

```bash
task cluster:secrets:export stack=dev | pass insert -m hetzner/dev/talos-secrets
```

Run by `task cluster:secrets:export`.
