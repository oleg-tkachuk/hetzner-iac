# Roadmap

What this repository intends to become, and in what order.

No dates. The order is by dependency and by the cost of being wrong, not by
calendar. Items marked **blocked** wait on a decision rather than on work, and
the decision is named.

## Recovery

The cluster itself is reproducible from a clone — the topology and every layer
are committed. Three things are not, and those are what recovery means here.

### A restore that has been rehearsed

Taking a snapshot is not a backup until restoring one has been done. The goal
is a written procedure, exercised on a throwaway cluster, that ends in a
working cluster.

### The cluster's identity held in a second place

The certificate authority the cluster is built around exists in exactly one
place. It is also what makes a snapshot usable, so a copy of it elsewhere is
the first half of any recovery.

### Volume data

Persistent volumes hold the only state no layer can recreate. The goal is both
a point-in-time copy of a volume and a way to bring back one workload's data
without touching the rest.

## Availability

### More than one control-plane node

One node survives no failure, and the API address moves when that node is
replaced. The goal is a control plane that outlives the loss of a member, at an
address that does not move.

## Security

### Deny by default on the network

The policies exist and are applied; the default deny that gives them meaning is
off, because closing the network without knowing every legitimate flow breaks
the cluster quietly. The goal is a closed network with its flows named.
**Blocked on:** flows that do not exist yet — see Delivery.

### Secrets come from a secret store

Credentials live in stack configuration: workable for one operator, wrong for a
cluster that outlives one.
**Blocked on:** which store holds them.

### Every workload declares what it needs

Most containers run with no resource limits, so any one of them can starve the
node the others are on.

### What runs is verified

Charts and images are pinned, which covers the supply chain as far as the pull.
Nothing checks provenance at admission.

## Delivery

### Something is reachable from outside

The ingress and the certificate machinery are deployed, and nothing uses them.
**Blocked on:** a domain.

### Workloads arrive through GitOps

Argo CD runs and reconciles nothing. The goal is that adding a workload is a
commit rather than a task.
**Blocked on:** which repository and path hold them.

### Drift is reported, not discovered

A change made by hand outside this repository stays invisible until the next
apply. The goal is a scheduled comparison that says what differs.

### Upgrades are exercised before they matter

Talos and Kubernetes upgrades have never been run here. The goal is to run each
one first on a cluster that can be discarded.

## Not planned

- **Another cloud.** Only the cluster tier is Hetzner-specific, but portability
  is not a goal and would cost the things that keep this simple.
- **A managed control plane.** Hetzner offers none — that is why this exists.
