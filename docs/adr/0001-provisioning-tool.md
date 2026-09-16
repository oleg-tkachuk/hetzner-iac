# ADR-0001: Provisioning with the Pulumi Go SDK

**Status:** Accepted
**Date:** 2026-09-09
**Deciders:** Oleg Tkachuk

## Context

Hetzner Cloud is the only target, and it has no managed Kubernetes: the
control plane is self-operated whatever else is decided. The estate is
operated by one person, who is also on call for it, so every moving part is a
recurring cost rather than a one-off.

## Decision

**Pulumi with the Go SDK** is the single source of truth for every Hetzner
Cloud resource: servers, networks, subnets, firewalls, load balancers,
volumes, DNS records and Storage Boxes.

## What it gives

**The infrastructure is Go, so it is testable.** Component resources make a
cluster one node in the graph, and typed outputs carry a contract across stack
boundaries. The cluster component runs under Pulumi's mock monitor, which is
how the firewall rules, the addressing and "a single control plane creates no
load balancer" are asserted with no Hetzner account and no cluster.

**One module behind every project.** The cluster tier and the six layers share
`internal/pkg` — the components, the chart registry, the output contract and
the tests ([ADR-0005](0005-internal-packages.md)).

**A preview before anything changes.** `pulumi preview` is the reviewable diff
every task runs first, and the policy pack in `policy/` runs over the resources
a program declares rather than the constructors it called.

## Consequences

- Pulumi state is in the critical path, which
  [ADR-0003](0003-state-and-secrets.md) decides.
- A Go runtime and the Pulumi CLI are prerequisites on any machine that
  applies.
- Infrastructure logic can be written cleverly rather than obviously. The
  answer is the gates in `internal/ci`, not a convention nobody checks.
