# recoverykit

Prints, on stdout, the parts of a cluster that live nowhere but Pulumi's state.

```bash
go run ./tools/recoverykit <stack>
```

Three parts, and each opens the next:

1. **talos-secrets** — the cluster CA. `talosctl bootstrap --recover-from`
   accepts an etcd snapshot only against these, so without it a snapshot
   restores nothing. Same value as [secrets](../secrets) prints alone.
2. **backup-outputs** — every output of `layers/60-backup`, secrets decrypted:
   the Storage Box and the restic repository password the snapshots are
   encrypted with.
3. **topology** — `cluster.<stack>.yaml`, which is gitignored because it names
   the networks the cluster is administered from, so a clone does not carry it.

## Why the second part matters more than it looks

The backups this repository already takes are not self-sufficient, and the
dependency runs the wrong way:

```
etcd snapshot on the Storage Box
  └─ encrypted by restic
       └─ repository password: random.NewRandomPassword in layers/60-backup's state
            └─ re-applying that layer generates a DIFFERENT one,
               which does not open the existing repository
```

So losing the state turns every snapshot into ciphertext nobody can open. This
is the set that breaks the circle: with it the snapshots are readable and a
cluster can be rebuilt without reaching Pulumi at all.

## What it is not

It is **not** a state backup. `task cluster:state:export` covers the resource
graph and answers a deleted stack — the likely failure, where the account still
works and the ciphertext can be imported back. This answers the other one: a
backend out of reach, which an export encrypted by that backend's own key does
not.

## stdout and only stdout

The same discipline as [secrets](../secrets) and [token](../token), and it
**refuses a terminal** for the same reason.

```bash
task cluster:recovery-kit stack=dev | pass insert -m hetzner/dev/recovery-kit
```

A part that cannot be read is named in the document rather than left out: a kit
is written on a good day and read on a bad one, and one that silently omitted
the backup credentials would leave the reader believing the snapshots are
recoverable.

Run by `task cluster:recovery-kit`.
