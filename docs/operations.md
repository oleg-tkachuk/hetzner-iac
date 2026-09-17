# Running it day to day

A cluster that already exists: stopping it, starting it, asking whether it
works, and reaching it with a plain kubectl. Upgrades, snapshots and restores
are [recovery.md](recovery.md) — a different question, asked at a different
moment. How a cluster is built is [the command reference](commands.md); why it
is shaped this way is [design.md](design.md).

## Stopping and starting

Two levels, not two halves. The namespace says which API answers, and each is
complete on its own terms:

| | Soft | Hard | Back on |
|---|---|---|---|
| the **instance** | `hcloud:shutdown` | `hcloud:poweroff` | `hcloud:poweron` |
| the **cluster** | `cluster:stop` | — | `hcloud:poweron` |

Restarting has three rungs rather than two, and which one to use is a question
about what is still answering:

| Rung | Needs | Use when |
|------|-------|----------|
| `cluster:reboot` | Talos answering | normally — etcd closes its log |
| `hcloud:reboot` | the kernel running | apid has stopped answering, the machine has not stopped |
| `hcloud:reset` | nothing | the node is gone; etcd recovers its log on the way back |

Which to reach for follows from the level. Stopping the **cluster** is a Talos
operation: `talosctl shutdown` brings etcd down cleanly and can cordon and
evict first. Halting the machine is its consequence, so the instance then
needs `hcloud:poweron` — not because the pair is split, but because a stopped
machine runs no apid and nothing but the provider can power it back on.

Stopping the **instance** says nothing to Kubernetes at all. `hcloud:shutdown`
presses the power button and Talos acts on the ACPI event; `hcloud:poweroff`
cuts power mid-write, and etcd recovers on the next boot instead of starting
clean. Prefer `cluster:stop` whenever Talos is answering; reach for these when
it is not.

**Stopping does not save money.** A Hetzner server is billed while it exists,
not while it runs — [their billing
documentation](https://docs.hetzner.com/cloud/billing/) is explicit that
servers are billed until they are deleted regardless of state. To stop paying,
destroy: `task cluster:destroy`. What stopping buys is a cluster that is
unreachable and unchanging, with its disks at rest.

Encrypted volumes do not complicate a power cycle. The LUKS key derives from
the node's own UUID, which survives one, so the disks unlock with no operator
— see [design.md](design.md#what-the-cluster-encrypts-and-what-it-does-not).

### These are conveniences, not a management interface

The `hcloud:` tasks are the shared library's
[`hcloud` module](https://github.com/oleg-tkachuk/taskfiles/blob/main/hcloud/README.md),
not this repository's own. They act on every server of the cluster at once,
selected by the `cluster=<name>` label that `internal/pkg/hetzner` stamps — the label is
why they are safe on a shared project and why they work unchanged on three
control planes.

The module knows nothing about Pulumi or the topology, so the two things it
cannot know are passed as commands rather than values: `HCLOUD_SELECTOR_CMD`
reads the cluster name through the real parser, `HCLOUD_TOKEN_CMD` decrypts the
token out of stack config. Both run inside the task, which is what keeps
`task --list` from reaching for a credential.

One exception to the fleet-wide rule: `hcloud:console` takes
`HCLOUD_SERVER=<name>`, because a console for five nodes at once is not a
thing. It refuses a name the selector does not cover, and prints what it has
instead — the project may hold servers this repository did not create, and the
API will happily open a console on one of them. What it prints is a short-lived
credential giving root-level console access, so keep it out of anything that
logs.

Everything else Hetzner offers is deliberately not wrapped — `hcloud server`
alone has rebuild, change-type, rescue mode, ISO attachment, backups,
snapshots, RDNS and per-server metrics. Each of those either fights Pulumi for
ownership of the server or means nothing against Talos, and metrics is marked
ALPHA upstream. Use the CLI directly for those:

```bash
export HCLOUD_TOKEN="$(go run ./tools/token dev)"
hcloud server --help
hcloud server describe platform-dev-control-plane-0
```

A wrapper per API call would be a second, worse CLI to keep in step with the
first.

## Checks worth running

| Task | Answers |
|------|---------|
| `task cluster:encryption:check` | are the system volumes really encrypted, or only configured to be |
| `task cluster:hubble` | what is the cluster's traffic, as flows |
| `task cluster:machine-config:check` | does Talos accept the machine-config patches |
| `task cluster:orphans` | is anything being billed that nothing claims |
| `task cluster:status` | are the nodes Ready, and is anything not Running |

Two of them exist because the failure they catch is silent.

`encryption:check` compares the configuration with the disk. Talos encrypts a
system volume only when the partition is empty, so applying the VolumeConfig
to a node that already exists is accepted, reports nothing, and leaves the
disk in plaintext. The probe's answer is the evidence: `luks` on an encrypted
volume, the filesystem itself on a plaintext one.

`orphans` compares the Hetzner project with the cluster. A StatefulSet's
claims outlive their Helm release by design, and destroying a cluster destroys
the API server that would have told the CSI driver to delete a volume — 160
GiB were found that way. It prints what it examined as well as what it found,
so "nothing to report" cannot read the same as "nothing was read".

It also answers when the cluster is already gone, which is when it is usually
wanted. No server carrying the cluster's label means no cluster, so an empty
set of claims is the truth rather than a failure to ask, and the report says
so before listing what the teardown left behind. While the servers are still
there, an unreachable cluster is still an error — empty claims would then be a
lie that invites deleting live volumes.

## Does the cluster actually work?

`pulumi up` going green is a different claim from "this cluster can run a
workload", and the gap is not hypothetical here: it went green on a three-node
cluster whose hcloud CSI controller was in CrashLoopBackOff. Every resource
created, every pod Running, and no volume obtainable — because nothing had
asked for one.

```bash
task cluster:smoke stack=dev
```

Four checks, each proving a different piece of cluster-tier wiring is working
rather than merely installed:

| Check | Proves |
|---|---|
| every node is `Ready` | the CNI is installed — Talos leaves a node `NotReady` until one is |
| a pod reaches a pod on another node | the CNI actually **routes** |
| a claim on `hcloud-volumes` reaches `Bound` | the CSI driver, end to end through the Hetzner API |
| every LoadBalancer Service has an address and somewhere to send it | the cloud controller manager |

The second one was added after the failure it would have caught. Pod-to-pod
traffic across nodes had no route at all for thirteen hours, and nothing said
so: every node `Ready`, every pod `Running`, and about a third of DNS queries
timing out. It surfaced as the CSI controller crash-looping — see
[design.md](design.md#how-pod-traffic-crosses-a-node-boundary) for the chain.

It works by asking the cluster's DNS from a node that runs **no** DNS replica,
so every backend it can reach is on another node and the query has to cross a
boundary to be answered. Choosing that node is the load-bearing part: ask from
a node with a local replica and the check passes on a cluster whose cross-node
traffic is dead, which is worse than not having it. On a single-node cluster
there is no such path, so it reports skipped — which is exactly why a
single-node cluster could not have exhibited the original failure.

The storage check applies a claim and a pod, waits for `Bound`, and deletes
both — including when the wait fails, which is when cleanup is usually
forgotten. It schedules a pod because `hcloud-volumes` is
`WaitForFirstConsumer`: a bare claim stays `Pending` forever on a perfectly
healthy cluster, so a check that applied only a claim would report a working
driver as broken.

### Skipped is not passed

A check that cannot run reports `○ skipped`, never a green tick. With no
LoadBalancer Service in the cluster there is nothing for the controller manager
to have done, and calling that success would be a green line that inspected an
empty list. The summary counts them apart:

```
✔ every node is Ready — 3 Ready
✖ a claim on hcloud-volumes reaches Bound — claim is Pending after 2m0s. Last event: …
○ every LoadBalancer Service has an address and somewhere to send it — no Service of type LoadBalancer exists…

3 checks, 1 skipped
```

That is real output from the dev cluster, and the middle line is the open CSI
fault.

Exit codes are `2` for "the checks ran and the cluster failed" and `1` for "the
checks could not run" — a missing kubeconfig, an unreachable API server. Both
`go run` and `task` flatten any non-zero child to their own `1`, so a caller
that needs the difference has to build the binary:
`go build -o smoke ./tools/smoke`.

## Reaching the cluster with a plain kubectl

Every task here passes `--kubeconfig` explicitly, and so does Pulumi, so none
of them depends on what the shell points at — a resource created against the
ambient config lands on whatever cluster that happens to be. A bare `kubectl`
is the exception, and there are two ways to give it this cluster.

Without touching any file, which is the safer one:

```bash
export KUBECONFIG=$HOME/.kube/config:$PWD/kubeconfig
```

kubectl merges at read time, so both sets of contexts appear.

Or add it once:

```bash
task cluster:kubeconfig:add stack=dev
```

Three entries through `kubectl config set-*`, not a merged file. The obvious
`kubectl config view --flatten` is wrong here: it rewrites the whole target
and inlines every other cluster's `certificate-authority` file into the
document — measured on a config holding an unrelated cluster, whose
`certificate-authority: /path/ca.crt` came back as `certificate-authority-data`.
That is somebody else's entry changed in order to add ours.

The certificate data is passed as base64 with `--set-raw-bytes=false`, which
is what keeps a cluster-admin key off the disk: the `--embed-certs` route
needs the key written to a temporary file first.

It backs the target up with a timestamp, refuses outright if a cluster of that
name already points somewhere else, and does not switch the current context —
it prints the command that would.
