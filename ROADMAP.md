# Roadmap

What this repository intends to become, and in what order.

No dates. The order is by dependency and by the cost of being wrong, not by
calendar. Every open item is **blocked** on a decision or on someone else,
and names which.

## Open

### Workloads arrive through GitOps

Adding a workload is a commit rather than a task once Argo CD is pointed at the
repository holding them: `gitops:repoURL`, with `gitops:path` and
`gitops:revision` beside it, public or private. Unset, Argo CD reconciles
nothing and says so.
**Blocked on:** which repository holds the workloads — a deployment decision.

### Every workload declares what it needs

Every chart but Argo CD sets a memory request and limit, and `charts render`
holds each container to it. Argo CD's repo-server grows while rendering
manifests and has rendered none yet.
**Blocked on:** Argo CD's first real sync, to measure it.

### Secrets come from a secret store

Credentials live in stack configuration: workable for one operator, wrong for a
cluster that outlives one. External Secrets is deployed and has no store.
**Blocked on:** which store holds them.

### Something is reachable from outside

Nothing here is left to build. `40-ingress` creates the `A` and `AAAA` records
for `metadata.domain`, or says they are somebody else's when the zone is hosted
elsewhere; `30-cluster-services` orders the certificate.
**Blocked on:** a domain in the topology. [domain.md](docs/domain.md) is what
to set.

### Every image is verified by signature

Argo CD, Dex, Cilium and KEDA sign only as Sigstore bundles, which
policy-controller cannot verify yet, so their policies stay at warn under
enforce. See [operations.md](docs/operations.md#which-images-may-run).
**Blocked on:** bundle verification upstream.

### The configuration contract moves to Talos 1.14

Nodes run Talos v1.14.2 and Kubernetes v1.37.1 on the v1.13.10 machine-config
contract, held by `talos.configVersion`: pulumi-talos 0.8.1 generates
configuration with Talos machinery v1.13.
**Blocked on:** a pulumi-talos release built on v1.14.

### An Arm cluster is proven

`talos.architecture: arm` is implemented and every chart publishes
`linux/arm64`; none of it has run on real hardware. In the same project and
location `cx23` creates and `cax11` is refused, although the API advertises
it — the refusal follows the architecture, not the region.
**Blocked on:** a Hetzner support answer.

## Done

- **A rehearsed restore.** An etcd snapshot uploaded off-site, fetched back and
  restored on dev's three control-plane nodes; see [recovery.md](docs/recovery.md).
- **The cluster's identity in a second place.** `task cluster:etcd:upload`
  stores the Talos secrets bundle beside every snapshot on the Storage Box.
- **Deny by default.** The default deny runs on dev with every platform flow
  named; a new stack opts in with `network-policy:enabled`.
- **Image verification.** Every image a chart installs has a
  ClusterImagePolicy, enforced in every namespace but `kube-system` and the
  policy-controller's own.
- **Drift is reported.** `task platform:drift` exits 1 on drift, run by hand:
  a hosted CI runner cannot reach APIs the firewall opens to
  `network.adminCIDRs` only.
- **Upgrades rehearsed.** Talos v1.13.10 → v1.14.2 node by node and Kubernetes
  v1.36.4 → v1.37.1, on dev, smoke passing.
- **Stable control-plane addresses.** Control-plane nodes hold explicit Primary
  IPs, so a replacement keeps the address the kubeconfig and talosconfig name.

## Not planned

- **Another cloud.** Only the cluster tier is Hetzner-specific, but portability
  is not a goal and would cost the things that keep this simple.
- **A managed control plane.** Hetzner offers none — that is why this exists.
- **Observability.** Metrics, logs and alerting are for whoever runs workloads
  on the cluster to choose and install. The smoke checks and
  `task platform:drift` verify the cluster and its platform, and are not
  monitoring.
- **Volume backup.** Left to the cluster's users, since everything else is
  rebuilt from this repository; [ADR-0006](docs/adr/0006-volume-backup.md)
  records the options.
