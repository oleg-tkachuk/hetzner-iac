# secrets

Prints a stack's Talos secrets bundle on stdout.

```bash
task cluster:secrets:export stack=dev | pass insert -m hetzner/dev/talos-secrets
go run ./tools/secrets <stack>
```

The bundle is the cluster's root of trust: the CA keys every node and client
certificate descends from, the bootstrap tokens, and the secretbox key that
encrypts Kubernetes Secrets inside etcd. It lives only in Pulumi's state, where
`Protect` keeps a destroy from taking it — which is not a second copy. An etcd
snapshot is useless without it: `talosctl bootstrap --recover-from` accepts a
snapshot only against the same secrets.

stdout only, as [token](../token) does, and it **refuses a terminal**: a
certificate authority in scrollback outlives the session. Needs `pulumi` in
PATH, logged in to the stack's backend.

Run by `task cluster:secrets:export`, and by `task cluster:etcd:upload`, which
pipes it into the restic repository on the Storage Box beside every snapshot —
encrypted under the repository's key, never written to disk.
`task cluster:secrets:download` prints it back.
