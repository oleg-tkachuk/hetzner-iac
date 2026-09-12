# Why it is shaped this way

Four decisions explain most of the repository. Each one has a cost, and the
cost is named.

## Each layer is its own Pulumi project

A layer can be previewed, applied and destroyed on its own, and reads the
cluster's kubeconfig through a `StackReference` rather than sharing state with
it. Upgrading Cilium does not mean planning a change to Argo CD.

Independence has a boundary worth stating: layers are independently
*appliable*, not order-free. On an empty cluster nothing schedules before the
cloud controller manager clears Talos's `uninitialized` taint, and nothing
networks before the CNI. `task platform:apply-all` walks them in order; the
order lives once, in the Taskfile, and CI derives its matrix from the same
list.

## The cluster is a committed file

`infra/cluster/cluster.<stack>.yaml` describes the topology, so a cluster is
reviewable in a diff before it exists and reproducible from a clone. It is
sparse — anything omitted keeps the default in `pkg/hetzner` — and it is
validated against the same code the Pulumi program runs, so the check cannot
drift from the thing it checks.

One field keeps that file out of git: `network.adminCIDRs` is the operator's
own address. `cluster.example.yaml` is committed with an RFC 5737 placeholder
instead.

The consequence is that CI cannot preview the cluster tier — a runner has no
topology. Cluster-tier drift is checked by hand with `task cluster:plan`.

## The cluster tier stops at "a Kubernetes API that answers"

It installs no CNI: Talos would otherwise install Flannel, which would then
have to be removed before Cilium could take over. Nodes are `NotReady` until
`layers/10-node-platform` runs. That is the handover point, not a failure — and
it is why that layer also owns the cloud controller manager, which cannot be
scheduled onto a node no CNI has made Ready.

## Every chart version is pinned in one place

`pkg/charts` is the registry; floating tags are rejected by validation rather
than by convention. `task charts:outdated` compares each pin against its
upstream repository, and Renovate opens one pull request per chart — see
[ci.md](ci.md#chart-upgrades-arrive-as-pull-requests).

# Versions

Both the Talos and the Kubernetes version are pinned in the topology, and
neither derives from the other. An empty `kubernetes.version` takes
`DefaultKubernetesVersion` — also pinned — rather than whatever the configured
Talos release happens to ship.

That is not caution for its own sake. Deriving one from the other made a Talos
patch bump able to move Kubernetes a whole minor with no diff and no decision,
and it did: the first bring-up landed on v1.36.0, new enough that
`kube-apiserver` had removed a flag the machine config was passing, and the
control plane never started.

Upgrading Talos means bumping `talos.version` in the topology, re-running
`task cluster:image-bake`, then `task cluster:upgrade-talos`. Nodes are
upgraded in place and never replaced, which is why the server resource ignores
changes to its image.

# The Hetzner token travels with the kubeconfig

The cluster tier exports the token and every layer reads it through the same
stack reference that carries the kubeconfig, so it is typed once.

The objection to exporting a credential — that it lands in the state of every
referencing stack — is already true of the kubeconfig and the talosconfig on
that channel, both strictly more powerful than an API token.

# Asking the real tool

Four checks run offline against the actual software rather than against this
repository's own assumptions, because that is where the expensive mistakes
hide. Each of these was found that way, and none would have failed a
`pulumi up` cleanly:

- Grafana pointed at Tempo's port 3100, which the chart does not expose.
- `machine.network.hostname`, which Talos rejects outright.
- A Talos version pinned ahead of what the provider's generator knows.
- A DaemonSet needing host access in a namespace Talos does not exempt from
  Pod Security Admission — its pods are never created, and Helm waits out its
  whole timeout with nothing to show.

`task verify` runs them all. They need `helm`, a `talosctl` matching the
pinned Talos minor, and a running Docker.

# What a run prints

Every layer logs through [pkg/pulumilog](../pkg/pulumilog), which borrows its
vocabulary from the [taskfiles](https://github.com/oleg-tkachuk/taskfiles)
repository so that `task` and `pulumi up` read as one tool:

| glyph | means | survives the run |
|-------|-------|------------------|
| `◉` | work starting | no |
| `✔` | work finished | no |
| `○` | deliberately not done | **yes** |
| `▲` | configured, and will not do what it looks like | **yes** |

The last two are the point. A layer that installs cert-manager and no
ClusterIssuer is the most confusing thing this repository can do — so those
lines go to Pulumi's permanent diagnostics and are still on screen when the
run ends. Progress lines are ephemeral, or the summary is one line per release
and nobody reads it.

The loudest of them today is Alertmanager. The chart's default route ends at a
receiver named `null`, so alerts are grouped, inhibited and then dropped:
Prometheus stores metrics, rules evaluate, alerts fire, and they reach nobody.
Every apply says so until a receiver exists.

What is *not* wrong, checked rather than assumed: the four scrape targets
Talos does not expose are disabled, and the chart removes their alert rules
along with them. Rendering with and without proves it — `KubeSchedulerDown`,
`KubeControllerManagerDown`, `KubeProxyDown` and `etcdMembersDown` are in the
chart's default output and absent from ours, and `absent()` drops from five
expressions to one (the API server, which should keep it). There are no
permanently firing alerts to silence.

`NO_COLOR` drops the escape codes and keeps the glyphs. A TTY check would be
wrong rather than merely unhelpful: a Pulumi program's output is captured by
the CLI over gRPC, so stdout is never a terminal.
