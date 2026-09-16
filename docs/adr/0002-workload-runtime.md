# ADR-0002: Talos Linux as the node operating system

**Status:** Accepted
**Date:** 2026-09-09
**Deciders:** Oleg Tkachuk

## Context

The nodes [ADR-0001](0001-provisioning-tool.md) creates have to run
Kubernetes, and the control plane is self-operated because Hetzner offers no
managed one. What dominates at this size is the class of incident where
somebody changed something on a box: it is invisible, it is per-node, and it
is what makes two machines that should be identical behave differently.

## Decision

**Talos Linux**, with the Kubernetes version **pinned in the topology** beside
the Talos version rather than taken from whatever the Talos release ships.

Three control-plane nodes with embedded etcd, on a private network behind a
firewall, with the Hetzner cloud controller manager for `LoadBalancer`
services and node lifecycle and the CSI driver for persistent volumes.

## What it gives

**There is nothing on a node to change.** No SSH and no package manager, so
node drift is not a category of failure. Machine configuration is applied over
the Talos API, and the OS and Kubernetes upgrade as one declarative operation.

**Kubernetes cannot move by accident.** An unpinned version would take
whatever the configured Talos release ships, so a Talos patch bump could carry
Kubernetes a whole minor with no diff and no decision — which is how this
cluster first came up on a minor new enough that kube-apiserver had dropped a
flag the machine config was passing.

## Consequences

- Hetzner has no custom-image upload API, so a Talos snapshot is baked out of
  band before a cluster can be built. `task cluster:image:bake` wraps it, and
  it is a real step to forget — the plan then fails with "no available Talos
  snapshot matches the selector".
- Debugging a node is `talosctl` and nothing else.
- The generator and the node have to agree: `pulumi-talos` embeds the
  machinery that GENERATES machine configuration, so the Talos version in the
  topology cannot move past the machinery the pinned provider carries. That is
  a version pin holding two things at once, and it is why a newer Talos is not
  simply a bump.
