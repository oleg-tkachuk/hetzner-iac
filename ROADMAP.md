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
Done on dev's three control-plane nodes, from the off-site copy: a snapshot
uploaded with `task cluster:etcd:upload`, the local file removed, fetched back
with `task cluster:etcd:download` and restored with `task cluster:etcd:restore`.
A ConfigMap written after the snapshot was gone, one written before it
remained, and smoke passed.

### The cluster's identity held in a second place

The certificate authority the cluster is built around existed in exactly one
place, and it is what makes a snapshot usable. `task cluster:etcd:upload` now
stores the Talos secrets bundle beside every snapshot in the restic repository
on the Storage Box, and `task cluster:secrets:download` reads it back.

### Volume data

Low priority: left to the cluster's users, since everything else is rebuilt
from this repository and not every volume needs a backup.
[ADR-0006](docs/adr/0006-volume-backup.md) records the options.

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

Every chart but Argo CD sets a memory request and limit, and `charts render`
holds each container to it. Argo CD waits for its first real sync: repo-server
grows while rendering manifests, and it has rendered none yet.

### What runs is verified

Every image a chart installs has a ClusterImagePolicy generated from
[`internal/pkg/imagepolicy`](internal/pkg/imagepolicy/images.yaml): a signer
where the publisher signs, a digest where it does not. They are enforced in
every namespace but `kube-system` and the policy-controller's own, and an image
no policy names is refused. Argo CD, Dex and KEDA sign only as Sigstore
bundles, which policy-controller cannot verify, so their policies stay at warn
until it can.

## Delivery

### Something is reachable from outside

Nothing here is left to build. `40-ingress` creates the `A` and `AAAA` records
for `metadata.domain`, looking the zone up rather than creating it — or says
the records are somebody else's to write when the DNS is hosted elsewhere.
`30-cluster-services` orders the certificate, staging or production by config.
Argo CD, the only service so far worth exposing, has its Ingress.

**Blocked on:** a domain in the topology. That is an input rather than work,
which is what **blocked** means above, and it is listed because the cluster
this repository describes has not been given one — not because anything is
missing. [domain.md](docs/domain.md) is what to set,
whether or not Hetzner serves the zone.

### A server keeps its address when it is replaced

Control-plane nodes hold explicit Primary IPs, so a replacement keeps the
address the kubeconfig and talosconfig name. Workers keep implicit ones, and
the ingress load balancer's address — the one DNS points at — is still the
load balancer's own.

### Workloads arrive through GitOps

Adding a workload is a commit rather than a task, once Argo CD is pointed at
the repository holding them: `gitops:repoURL`, with `gitops:path` and
`gitops:revision` beside it. Unset, Argo CD installs and reconciles nothing,
and says so.

A private repository works too: `gitops:repoSSHPrivateKey`, or
`gitops:repoUsername` with `gitops:repoPassword`, become the Secret Argo CD
authenticates with. The layer refuses the combinations Argo CD would accept and
then fail on — a key against an https:// URL, half of a username and password,
a credential with no repository — because each of those surfaces as an
authentication error in a UI minutes after a successful apply.
**Blocked on:** nothing here — which repository is a deployment decision, and
this layer no longer has an opinion about it.

### Drift is reported, not discovered

A change made by hand outside this repository stays invisible until the next
apply. `task platform:drift` says what differs, from each stack's refresh
preview, and exits 1 on drift. It is run by hand, before a change, by
decision: a refresh reads the Kubernetes and Talos APIs, which the firewall
opens to network.adminCIDRs only, so a hosted CI runner cannot reach them.

### Upgrades are exercised before they matter

The goal is to run each one first on a cluster that can be discarded.
Talos is done: dev went from v1.13.10 to v1.14.2 one control-plane node at a
time with `task cluster:upgrade:talos`, etcd healthy throughout, and the
configuration kept on the v1.13.10 contract through `talos.configVersion`.
**Blocked on:** Kubernetes 1.37, which needs the v1.14 contract. pulumi-talos
0.8.1 generates configuration with Talos machinery v1.13.0; the next step is a
release built on terraform-provider-talos 0.12.0, which carries v1.14.

## Not planned

- **Another cloud.** Only the cluster tier is Hetzner-specific, but portability
  is not a goal and would cost the things that keep this simple.
- **A managed control plane.** Hetzner offers none — that is why this exists.
- **Observability.** Metrics, logs and alerting are for whoever runs workloads
  on the cluster to choose and install. This repository stops at the cluster
  and its platform; the smoke checks and `task platform:drift` verify those,
  and are not monitoring.
