# recoverykit

Prints, on stdout, the parts of a cluster that live nowhere but Pulumi's state.

```bash
task cluster:recovery-kit stack=dev | pass insert -m hetzner/dev/recovery-kit
go run ./tools/recoverykit <stack>
```

Three parts, and each opens the next:

1. **talos-secrets** — the cluster CA. `talosctl bootstrap --recover-from`
   accepts an etcd snapshot only against these. Same value as
   [secrets](../secrets) prints alone.
2. **backup-outputs** — every output of `infra/backup`, secrets decrypted:
   the Storage Box and the restic repository password the snapshots are
   encrypted with. That password is generated into the tier's state, so losing
   the state makes every snapshot unreadable — see
   [recovery.md](../../docs/recovery.md#the-three-parts-of-a-backup).
3. **topology** — `cluster.<stack>.yaml`, which is gitignored because it names
   the networks the cluster is administered from, so a clone does not carry it.

It is **not** a state backup: `task cluster:state:export` covers a deleted
stack. This covers a backend out of reach.

stdout only, and it **refuses a terminal**, as [secrets](../secrets) and
[token](../token) do.

A part that does not exist yet — a backup tier never applied, a topology never
written — is named in the document rather than left out, so the reader does
not believe snapshots are recoverable when they are not. A part that exists
and could not be read — a timeout, an expired login — fails the command.

Needs `pulumi` in PATH, logged in to the stack's backend.

Run by `task cluster:recovery-kit`.
