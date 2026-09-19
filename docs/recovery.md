# Upgrades, snapshots and restores

What to read when a version moves or something is wrong. Day-to-day operation
— stopping, starting, the checks — is [operations.md](operations.md).

## Upgrades and backups

| Task | Does |
|------|------|
| `task cluster:etcd:restore` | restore etcd from a snapshot; wipes the control plane first, asks first |
| `task cluster:etcd:snapshot` | snapshot etcd into `.backups/`, read it back, record what it holds |
| `task cluster:etcd:upload` | upload a snapshot to the Storage Box with restic; keeps the last ten |
| `task cluster:secrets:export` | print the Talos secrets bundle, to pipe into a password store |
| `task cluster:upgrade:k8s` | upgrade Kubernetes in place; asks first |
| `task cluster:upgrade:talos` | upgrade Talos, one node at a time; asks first |

Both upgrades are Talos operations and both ask before they start. The Talos
version comes from the topology, not the task: bump `talos.version`, run
`task cluster:image:bake`, then upgrade — the image selector keys off the
version label, so a bump without a bake fails at plan time rather than
halfway.

`talos.architecture` needs the same re-bake, and for a sharper reason. The
version lives in a label; the architecture does not — Hetzner records it as a
field on the image. The bake asks about both, so a project holding an x86
snapshot and a topology asking for `arm` bakes a second one instead of
reporting the first as good enough. It did the latter until this was fixed, and
the pair of steps then pointed at each other: the bake said "already present"
and apply said "run `task cluster:image:bake`".

The bake also runs in the topology's `placement.location`, not
`hcloud-upload-image`'s default of `fsn1`. It works by creating a real server,
so the location has to be one that offers the server type — and for Arm the
default was the wrong one: Hetzner reports the `cax` line as *supported but not
available* in `fsn1`, while `hel1` and `nbg1` report it available. See
[configuration.md](configuration.md#cpu-architecture) for what Arm costs, which
locations have it, and the probe to run before planning an Arm cluster.

### The three parts of a backup

An etcd snapshot on its own restores nothing. `talosctl` accepts one only
against the same cluster secrets, and the `secrets` resource inside it is
ciphertext under the secretbox key that lives in those secrets. The bundle is
not an accessory to the snapshot; it is what makes the snapshot mean anything.

The bundle exists in exactly one place — Pulumi's state — where `Protect`
stops a destroy from taking it. That is not a second copy, and losing access
to the state backend loses the cluster's root of trust with it.

    task cluster:secrets:export stack=dev | pass insert -m hetzner/dev/talos-secrets

It prints to stdout and nothing else, and refuses a terminal: the one thing
worse than having no copy of a certificate authority is having one in
scrollback. Store it where the Hetzner token already lives.

Re-export it only if the bundle is ever regenerated, which nothing but
`task cluster:secrets:destroy` does.

The third part is the one that is easy to miss, because it makes the other two
unreadable rather than incomplete. restic encrypts the repository on the
Storage Box with a password that `infra/backup` **generates into its own
Pulumi state** — nothing types it, and re-applying that tier produces a
different one that does not open the existing repository. So the state holds
both the key to the snapshots and the secrets that make a restored snapshot
mean anything, and losing it turns every snapshot on the box into ciphertext
nobody can open.

One command takes all three out together:

    task cluster:recovery-kit stack=dev | pass insert -m hetzner/dev/recovery-kit

The bundle, every generated output of the backup tier, and the topology file —
which is gitignored, because it names the networks the cluster is administered
from, so a fresh clone does not carry it. It refuses a terminal for the same
reason `secrets:export` does, and a part it could not read is named in the
document rather than left out: a kit is written on a good day and read on a
bad one.

### Uploading a snapshot

`task cluster:etcd:snapshot` leaves the snapshot and its `.info` in `.backups/`
on the machine that ran it, which is one disk failure from having no backup at
all. `task cluster:etcd:upload` sends both to the Storage Box that
`infra/backup` creates:

```bash
task cluster:etcd:upload stack=dev trust_host_key=yes   # first time only
task cluster:etcd:upload stack=dev
```

With no `snapshot=`, it takes the newest file in `.backups/`.

restic does the work — upload, deduplication, encryption, retention and
verification — and rclone is only its transport: restic's own SFTP backend
speaks key authentication, and the box's credential is a generated password.
Both are in the `Brewfile`.

Nothing is configured by hand. The host, the login, both passwords and the path
are stack outputs of `infra/backup`, read through
`pulumi stack output --show-secrets`, and the rclone remote is assembled in the
command's own environment, so no credential is written to a config file.

The first run needs `trust_host_key=yes`, which reads the box's host key with
`ssh-keyscan` into a gitignored `.known_hosts` and prints its fingerprints —
compare them with the ones the Hetzner console shows for the box. Every run
after that verifies against that file and refuses a key that has changed.

**Copy the repository password out of the stack once.** It encrypts the
repository, so the uploads are unreadable without it:

```bash
pulumi -C infra/backup -s dev stack output backupRepositoryPassword --show-secrets
```

`task destroy` cannot take the Storage Box, because the backup tier is not in
the walk it destroys — not because of the box's delete protection, which stops
a delete from the console and not one from `pulumi destroy`. `task
backup:destroy` does take it, snapshots and all. And Pulumi's state is what
holds that password, so a lost state leaves the uploads on the box as bytes
nothing can read. Put it in the same password store
as `task cluster:secrets:export`, which is the other half a restore needs.

### Restoring

    task cluster:etcd:restore stack=dev snapshot=.backups/etcd-<stamp>.db

This is the procedure Talos documents, with nothing on top: wipe the EPHEMERAL
partition of every control-plane node, wait for each to come back with etcd in
`Preparing`, then bootstrap one of them from the snapshot. The others rejoin
once the control-plane endpoint answers.

Three things it does before touching anything:

- **checks the snapshot,** through the library that defines the format. A
  snapshot is a bbolt database, so opening it read-only validates the magic,
  the format version, the page size and the meta checksum; the buckets etcd
  puts there — `key`, `meta`, `members`, `cluster` — are what say it is an
  etcd snapshot rather than somebody else's database. It reports the revision
  count and the consistent index, so a snapshot can be told from another one;
- **finds the nodes through Hetzner,** not `talosctl get members`, which needs
  the etcd that is broken;
- **asks.** Everything written after the snapshot is gone.

The node list comes from the `role=control-plane` label, so this works
unchanged on one node or three — three is what ships.

Afterwards the cluster converges on its own, and the sequence is worth knowing
because the middle of it looks like a failure. Measured on a single-node
cluster, from a 45 MB snapshot:

| | |
|---|---|
| the task itself | about 1m40s — reset, reboot, wait for `Preparing`, bootstrap |
| the API answers | ~10s after the bootstrap |
| the node | `NotReady,SchedulingDisabled` for ~50s, then `NotReady`, then `Ready` at ~1m40s |
| workloads | back to the pre-restore count by ~3m, with no operator action |

Nothing needed re-applying: the snapshot holds the Helm release state as well
as the workloads, so the layers were already what they had been. Re-apply only
if something was created after the snapshot was taken — and that is exactly
what the restore cannot bring back.

Each snapshot is read back before the task reports success, and what it holds
is written beside it as `<snapshot>.info`:

    taken:     20260913T140204Z
    talosctl:  snapshot info: hash 68be579d, revision 39170, total keys 1616, …
    read back: …/etcd-20260913T140204Z.db — 45273120 bytes, 3278 revisions, consistent index 6028

A snapshot nobody has opened is a file of the right size, and the moment to
find that out is not the incident it was taken for. `cluster:etcd:restore`
prints that record beside what it reads back itself, so a file that changed
after it was written shows up before the wipe rather than after.

The two numbers are different counters and neither is the other: `revision` is
the MVCC revision, `consistent index` is how far raft had applied. The
consistent index also restarts after a recovery bootstrap, because that begins
a new raft cluster — so it tells two snapshots of one cluster apart and says
nothing across a restore.

The snapshots still write to the operator's machine, on no schedule, with no
copy anywhere else. That half is a gap, not a design.

## What to keep, and what each thing answers

Two copies, and they answer different failures. Keeping only the cheaper one
leaves the worse failure uncovered.

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
That is enough for the first row — the account still works, so the export can
be imported back — and no use at all for the second, which is why the recovery
kit is a separate command rather than a flag on this one.

`--show-secrets` would make the export self-sufficient and put the cluster CA
and the Hetzner token in a file on a disk. The kit is the same content through
a pipe instead, which is where a certificate authority belongs.

A third option closes the gap differently: give the stack a passphrase secrets
provider, so the ciphertext is decryptable by something you hold and the export
alone answers both rows. `Pulumi.<stack>.yaml` is gitignored here, so the
objection that makes that unsafe on a committed file does not apply —
[configuration.md](configuration.md#one-rule-changes-with-it) has what it
costs.
