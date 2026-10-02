# ADR-0006: Backing up volume data is the platform user's responsibility

**Status:** Accepted
**Deciders:** Oleg Tkachuk

## Context

Everything this repository builds can be rebuilt from it: the cluster from the
topology and the cluster tier's state, the platform from its layers, the
cluster's identity from etcd snapshots and the Talos secrets bundle, which
`cluster:etcd:upload` stores off-site in the backup tier's Storage Box.

What it cannot rebuild is data a workload writes to a PersistentVolume. Whether
that data needs a backup, how often and how consistently, depends on the
workload: a cache or a CI runner needs none, a database needs one taken with
the database's own tool. Those workloads are the cluster users', delivered
through `50-gitops` rather than this repository.

The volumes are `hcloud-volumes`: `ReadWriteOnce`, with no snapshots — neither
Hetzner Cloud nor the hcloud CSI driver offers them — so any backup is
file-level, taken from the node the volume is mounted on.

## Decision

**This repository does not back up volume data.** The cluster's users choose
whether and how, for the workloads that need it. It is low priority here, and
revisited only if a backup becomes something every cluster needs.

Two things stay in place because they cost nothing and help whoever does it:
the `hetzner-iac/holds-data` namespace label, which `cluster:smoke` holds to a
storage class that retains its volumes, and the backup tier's Storage Box,
which can take another subaccount and repository.

## Options, for whoever needs them

Evaluated against what this platform already runs.

| | Writes to | `ReadWriteOnce` | Restores | Images under enforce |
|---|---|---|---|---|
| **k8up** with an `rclone serve restic` bridge | the Storage Box over SFTP — k8up has no SFTP backend, so rclone serves restic's REST API over it | a backup job per node, beside the mounted volume | a `Restore` into one PVC | k8up is signed keylessly; rclone is unsigned and needs a digest pin |
| **Velero** with the kopia uploader | S3 only: Hetzner Object Storage, a second billed product | a node-agent DaemonSet reading the node's pod volumes | a namespace | unsigned |
| **A restic CronJob** per volume | the Storage Box, the way `cluster:etcd:upload` does | affinity to the workload's pod, per volume | a script | unsigned |

k8up fits best: one destination and one format with the etcd snapshots,
`rclone serve restic --append-only` keeps a backup job from deleting history,
and a restore targets one workload. Two limits come with it: k8up, like
Velero, backs up only volumes a running pod has mounted, so a workload scaled
to zero is skipped; and a database needs a dump hook such as
`k8up.io/backupcommand` to be consistent, whichever tool takes the copy.

## Consequences

- A workload's data survives its node being replaced, because the volume is
  reattached to the new one; it does not survive the volume being deleted,
  unless its owner backed it up.
- The recovery documentation covers the cluster and the platform, not
  workload data.
