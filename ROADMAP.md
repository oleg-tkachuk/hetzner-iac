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

### An Arm cluster is proven

`talos.architecture: arm` is implemented: the factory builds the `arm64` image,
the bake asks for it per architecture, the server types and their defaults
follow the field, and every chart the platform installs publishes `linux/arm64`.
None of it has run on real hardware.

The region is not the obstacle. Hetzner sells the `cax` line in `fsn1`, `nbg1`
and `hel1`, this cluster already lives in `hel1`, and the API reports `cax11`
as available there — and refuses every create anyway. One pair of requests
places the fault: in the same project and location, `cx23` creates and `cax11`
is refused, so it follows the architecture rather than the location.
**Blocked on:** a Hetzner support answer for why `cax` is refused where their
own API advertises it. Not on work here, and not on waiting for capacity.

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

### Something is reachable from outside — the parts that are not the domain

`40-ingress` creates the `A` and `AAAA` records for `metadata.domain` in
`metadata.dnsZone`, looking the zone up rather than creating it, and
`30-cluster-services` orders the certificate — staging or production, by
config. What is left is a domain to put in the topology.

### A server keeps its address when it is replaced

Public addresses are implicit and deleted with their server, and servers here
are replaced delete-first. An explicit primary IP survives that; it does not
make a load balancer's address stable, which is the one DNS would point at.

### Workloads arrive through GitOps

Adding a workload is a commit rather than a task, once Argo CD is pointed at
the repository holding them: `gitops:repoURL`, with `gitops:path` and
`gitops:revision` beside it. Unset, Argo CD installs and reconciles nothing,
and says so.
**Blocked on:** nothing here — which repository is a deployment decision, and
this layer no longer has an opinion about it.

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
