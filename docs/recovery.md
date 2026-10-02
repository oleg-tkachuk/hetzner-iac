# Upgrades, snapshots and restores

What to read when a version moves or something is wrong. Day-to-day operation
— stopping, starting, the checks — is [operations.md](operations.md).

## Upgrades and backups

| Task | Does |
|------|------|
| `task cluster:etcd:download` | fetch a snapshot back from the Storage Box into `.backups/` (`id=`, default the newest), verified |
| `task cluster:etcd:restore` | restore etcd from a snapshot; wipes the control plane first, asks first |
| `task cluster:etcd:snapshot` | snapshot etcd into `.backups/`, read it back, record what it holds |
| `task cluster:etcd:upload` | upload a snapshot to the Storage Box with restic, with the Talos secrets bundle beside it; keeps the last ten |
| `task cluster:recovery-kit` | the secrets bundle, the backup credentials and the topology, in one document for a password store |
| `task cluster:secrets:download` | print the secrets bundle stored on the Storage Box, to pipe into a password store |
| `task cluster:secrets:export` | print the Talos secrets bundle, to pipe into a password store |
| `task cluster:state:export` | every tier's Pulumi state into `.backups/state/`, secrets left encrypted |
| `task cluster:upgrade:k8s` | upgrade Kubernetes in place; asks first |
| `task cluster:upgrade:talos` | upgrade Talos, one node at a time; asks first |

Both upgrades are Talos operations and both ask before they start. The Talos
version comes from the topology, not the task: on a running cluster pin
`talos.configVersion` to the version it was created with, bump
`talos.version`, run `task cluster:image:bake`, then upgrade. Without the pin
the configuration is generated for the new version, which Talos 1.14 rejects;
without the bake the image selector finds no snapshot at plan time rather
than halfway.

Changing `talos.architecture` needs the same re-bake: the bake checks both the
version and the architecture, and bakes in the topology's
`placement.location`, which must offer the server type. See
[configuration.md](configuration.md#cpu-architecture) for which locations have
Arm and the probe to run before planning an Arm cluster.

### The three parts of a backup

An etcd snapshot on its own restores nothing. `talosctl` accepts one only
against the same cluster secrets, and the `secrets` resource inside it is
ciphertext under the secretbox key that lives in those secrets.

The bundle lives in Pulumi's state, where `Protect` stops a destroy from
taking it, and `cluster:etcd:upload` stores a copy beside each snapshot in the
restic repository. Neither is a copy you hold: losing access to the state
backend and to the repository key loses the cluster's root of trust.

    task cluster:secrets:export stack=dev | pass insert -m hetzner/dev/talos-secrets

It prints to stdout and nothing else, and refuses a terminal, so the
certificate authority never lands in scrollback. Store it where the Hetzner
token already lives. Re-export it only if the bundle is regenerated, which
nothing but `task cluster:secrets:destroy` does.

The third part is the restic repository key, and without it the other two are
unreadable. `infra/backup` generates it into its own Pulumi state and keeps it
in a protected stash, so re-applying the tier keeps it. Destroying the tier
with `task backup:destroy ignore_protect=yes` discards it, and a fresh apply
then generates a key that does not open the existing repository. Losing the
state turns every snapshot on the box into ciphertext nobody can open.

One command takes all three out together:

    task cluster:recovery-kit stack=dev | pass insert -m hetzner/dev/recovery-kit

The bundle, every generated output of the backup tier, and the topology file,
which is gitignored, so a fresh clone does not carry it. It refuses a terminal
for the same reason `secrets:export` does, and a part it could not read is
named in the document rather than left out.

### Uploading a snapshot

`task cluster:etcd:snapshot` leaves the snapshot and its `.info` in `.backups/`
on the machine that ran it. `task cluster:etcd:upload` sends both to the
Storage Box that `infra/backup` creates, with the Talos secrets bundle beside
them:

```bash
task cluster:etcd:upload stack=dev trust_host_key=yes   # first time only
task cluster:etcd:upload stack=dev
```

With no `snapshot=`, it takes the newest file in `.backups/`. It needs restic
and rclone, both in the `Brewfile`. The host, the login, both passwords and the
path are stack outputs of `infra/backup`; nothing is configured by hand and no
credential is written to a config file.

The first run needs `trust_host_key=yes`, which reads the box's host key with
`ssh-keyscan` into a gitignored `.known_hosts` and prints its fingerprints —
compare them with the ones the Hetzner console shows for the box. Every run
after that verifies against that file and refuses a key that has changed.

**Copy the repository password out of the stack once.** It encrypts the
repository, so the uploads are unreadable without it:

```bash
pulumi -C infra/backup -s dev stack output backupRepositoryPassword --show-secrets
```

Put it in the same password store as `task cluster:secrets:export`, which is
the other half a restore needs.

Only `task backup:destroy ignore_protect=yes` can take the Storage Box, and it
takes the snapshots with it — see [commands.md](commands.md#backup).

### Restoring

When this machine has no snapshot, fetch one from the Storage Box first:

    task cluster:etcd:download stack=dev

Then restore it:

    task cluster:etcd:restore stack=dev snapshot=.backups/etcd-dev-<stamp>.db

This is the procedure Talos documents, with nothing on top: wipe the EPHEMERAL
partition of every control-plane node, wait for each to come back with etcd in
`Preparing`, then bootstrap one of them from the snapshot. The others rejoin
once the control-plane endpoint answers.

Three things it does before touching anything:

- **checks the snapshot:** that it is a valid bbolt database holding etcd's
  buckets — `key`, `meta`, `members`, `cluster` — and reports its revision and
  consistent index;
- **finds the nodes through Hetzner,** by the `role=control-plane` label, not
  `talosctl get members`, which needs the etcd that is broken;
- **asks.** Everything written after the snapshot is gone.

Afterwards the cluster converges on its own. The node passes through
`NotReady,SchedulingDisabled` and `NotReady` before `Ready`, which is not a
failure, and the workloads come back with no operator action: the snapshot
holds the Helm release state as well as the workloads. Re-apply only if
something was created after the snapshot was taken — that is what the restore
cannot bring back.

Each snapshot is read back before `etcd:snapshot` reports success, and what it
holds is written beside it as `<snapshot>.info`. `cluster:etcd:restore`
prints that record beside what it reads back itself, so a file that changed
after it was written shows up before the wipe rather than after.

`revision` is the MVCC revision and `consistent index` is how far raft had
applied. The consistent index restarts after a restore, so it tells two
snapshots of one cluster apart and says nothing across a restore.

## What to keep, and what each thing answers

Two copies, and they answer different failures.

| What broke | What recovers it |
|---|---|
| Somebody deleted a stack | the state export, read back with `pulumi stack import` |
| The state backend is out of reach — a lost account, a removed organization | the recovery kit: the bundle, the backup credentials, the topology |
| The laptop is gone | nothing is lost, as long as the topology is not only there |
| Pulumi Cloud is down | nothing; wait |

```bash
task cluster:state:export stack=dev     # every tier, into .backups/state/
```

That writes **ciphertext**: `pulumi stack export` leaves secrets encrypted by
the stack's secrets provider, which by default here is Pulumi Cloud's own key.
It answers the first row and not the second, which is what the recovery kit is
for. The kit carries the same secrets through a pipe rather than in a file on a
disk.

A passphrase secrets provider would let the export alone answer both rows —
[configuration.md](configuration.md#one-rule-changes-with-it) has what it
costs.
