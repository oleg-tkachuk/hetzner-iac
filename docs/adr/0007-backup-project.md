# ADR-0007: The backup destination lives in a Hetzner project of its own

**Status:** Accepted
**Date:** 2026-10-25
**Deciders:** Oleg Tkachuk

## Context

`infra/backup` created the Storage Box with a provider built from the cluster
tier's token. A Hetzner token reaches every resource in its project, the
Storage Box API included, and it can clear a box's delete protection. That
token is also a stack output every layer holds and a Secret in `kube-system`,
the one namespace image verification does not enforce.

So the recovery point shared the fate of what it protects: whatever
compromised the cluster could delete the box, the box's own snapshots and the
secrets bundle stored beside each etcd snapshot. Only the recovery kit in a
password store would have been left.

A second token in the same project fixes nothing. Tokens are scoped to a
project, not to a resource, so any token the cluster's project issues deletes
the box as easily as the first.

## Decision

**The Storage Box lives in a Hetzner project of its own**, reached with a
token that project issued. `infra/backup` reads it from `backup:hcloudToken`,
set with `task backup:token`, and has no fallback to the cluster's token.

**The program checks the project, not the token string.** It lists the
cluster's servers through the backup provider and refuses to continue when it
sees any. Hetzner has no call that names a token's project; whether the
cluster is visible is the question that answers it.

The tier still reads the cluster's name and location from the cluster tier's
stack. Nothing else crosses.

## Considered and declined: append-only writes

`rclone serve restic --append-only` refuses deletes, but only when the proxy
holds the credential and the writer does not. Today the writer is the
operator's machine, and there is no host between it and the box to run the
proxy on.

What already does that job is the box's own snapshot plan: daily, ten kept.
The subaccount cannot reach those snapshots, because it is confined to its
directory and ZFS access is off. With the box in another project, the cluster's
token cannot reach them either. A deleted or overwritten upload is recoverable
from the box for ten days. The window is up to 24 hours of uploads since the
last box snapshot.

## Consequences

- One more credential to keep: the backup project's token, in the backup
  stack's config and state. That stack is now the one place holding it.
- Creating a backup destination needs a manual step first, because Hetzner
  has no API for creating a project.
- `internal/ci/tiers_test.go` now holds two halves. The cluster tier is the
  only project configured with `hcloud:token`. The backup tier is the only one
  declaring a token of its own, and its source never reads the cluster's.
- `task cluster:orphans` reads the cluster's project, so it no longer sees the
  box. A box left behind in the cluster's project by an earlier backup tier is
  reported as an orphan, which is what it is.
- Moving an existing backup tier means a new box, and a new restic repository
  key. [recovery.md](../recovery.md#moving-the-backup-tier-to-its-own-project)
  has the steps.
