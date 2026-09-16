# ADR-0004: Repository structure and environment separation

**Status:** Accepted
**Date:** 2026-09-09
**Deciders:** Oleg Tkachuk

## Context

What dominates the layout is **blast radius**. An estate operated by one person
needs it to be structurally impossible to apply a change meant for one
environment to another by forgetting a word, because mechanisms that rely on
remembering are the ones that fail at 03:00.

## Decision

**A Pulumi project per concern, and an environment is a stack rather than a
directory.**

```
infra/cluster/          the cluster tier — the only project that talks to
                        the Hetzner API. Project `hetzner-cluster`
layers/10-node-platform/    Cilium, hcloud CCM and CSI
layers/20-network-policy/   Cilium network policy
layers/30-cluster-services/ cert-manager, external-secrets, metrics-server
layers/40-ingress/          Traefik, the ingress load balancer, DNS records
layers/50-gitops/           Argo CD
layers/60-backup/           Storage Box for etcd snapshots
policy/                 the CrossGuard pack every project is previewed against
internal/pkg/           the implementation every project imports
internal/ci/            the repository's own gates
tools/                  single-purpose Go programs the tasks call
tasks/                  the task definitions, one file per area
test/e2e/               what is checked against a running cluster
```

Each project holds its own state per stack, so a layer's apply cannot touch
another layer's resources, and one environment's state is not a slice of
another's.

**Everything that shapes a cluster is in one file per environment**,
`infra/cluster/cluster.<stack>.yaml`, validated by a schema as it is typed and
by the same Go code the program runs when it applies. Environments are not
required to be identical: one control-plane node in a scratch stack and three
in production is a different value in that file, not a different code path.

**Every cluster and layer task requires `stack=`, and there is no default.** A
missing one fails with what to type, rather than picking an environment:

```
cluster:apply needs a stack, and there is no default.

    task cluster:apply stack=dev
```

**A layer is pointed at a cluster by deriving the reference, not by typing
it.** `task platform:init stack=<stack>` reads the cluster tier's own stack
name and writes `<org>/hetzner-cluster/<stack>` into each layer's config. A
hand-typed reference is accepted in silence by everything — nothing compares
the two halves — and that is how five development layers install into a
production cluster.

## Consequences

- The blast radius of a mistake is one project in one environment.
- Two things have to agree for a layer to reach a cluster: the stack name and
  the derived reference. Both are written by one command.
- Adding an environment is `task cluster:token stack=<name>` and a topology
  file; nothing is copied and no directory is created.
- The layers are ordered by dependency, and the order is a property of the
  numbering rather than of a document. `task platform:apply layer=all` walks
  it.
- A change to `internal/pkg` reaches every project at once. That is the point
  — one chart registry, one output contract — and also why that package has
  gates of its own.
