# hetzner-iac

[![ci](https://github.com/oleg-tkachuk/hetzner-iac/actions/workflows/ci.yaml/badge.svg?branch=main)](https://github.com/oleg-tkachuk/hetzner-iac/actions/workflows/ci.yaml)
[![release](https://img.shields.io/github/v/release/oleg-tkachuk/hetzner-iac?sort=semver&label=release)](https://github.com/oleg-tkachuk/hetzner-iac/releases/latest)
[![go](https://img.shields.io/github/go-mod/go-version/oleg-tkachuk/hetzner-iac?logo=go&logoColor=white&label=go)](go.mod)
[![license: MIT](https://img.shields.io/github/license/oleg-tkachuk/hetzner-iac?label=license)](LICENSE)

[![Pulumi](https://img.shields.io/badge/Pulumi-8A3391?logo=pulumi&logoColor=white)](https://www.pulumi.com)
[![Talos](https://img.shields.io/badge/Talos%20Linux-FF7300?logo=talos&logoColor=white)](https://www.talos.dev)
[![Cilium](https://img.shields.io/badge/Cilium-F8C517?logo=cilium&logoColor=black)](https://cilium.io)
[![Argo CD](https://img.shields.io/badge/Argo%20CD-EF7B4D?logo=argo&logoColor=white)](https://argo-cd.readthedocs.io)
[![Hetzner Cloud](https://img.shields.io/badge/Hetzner%20Cloud-D50C2D?logo=hetzner&logoColor=white)](https://www.hetzner.com/cloud)

Kubernetes on Hetzner Cloud, built with the Pulumi Go SDK.

Hetzner has no managed Kubernetes, so this repository builds the cluster
itself — a Talos control plane on a private network — and then deploys the
platform onto it in independent, idempotent layers.

```
infra/cluster              the only project that talks to the Hetzner API
  └─ exports kubeconfig ──► layers/10-node-platform       Cilium, hcloud CCM + CSI
                            layers/20-network-policy      Cilium network policy (opt-in)
                            layers/30-cluster-services    cert-manager, ESO, metrics-server
                            layers/40-ingress             Traefik      
                            layers/50-gitops              Argo CD
```

## Contents

- [Quick start](#quick-start) — from nothing to a running platform
- [Prerequisites](#prerequisites) — what has to be installed
- [Commands](#commands) — the handful worth knowing
- [Configuration](#configuration) — the topology file and stack config
- [Documentation](#documentation) — the rest, by document
- [Layout](#layout) — where things live in the tree
- [Status](#status) — what works and what is not done
- [License](#license)

## Quick start

You need three things: a Hetzner Cloud API token with read+write scope, a
[Pulumi Cloud](https://app.pulumi.com/signup) account for state — free for an
individual, and `pulumi login` is how you get one on this machine — and the
tools in [Prerequisites](#prerequisites). State can live in your own S3 bucket
instead: [configuration.md](docs/configuration.md#keeping-state-in-your-own-s3-bucket).

Everything below creates **billable** Hetzner resources; `task cluster:destroy`
and `task platform:destroy-all` remove them.

```bash
# 1. Describe the cluster. Set network.adminCIDRs to the address you apply
#    from: Talos configuration goes over the Talos API, and a host outside
#    that list hangs with the port filtered.
cp infra/cluster/cluster.example.yaml infra/cluster/cluster.dev.yaml
$EDITOR infra/cluster/cluster.dev.yaml

# 2. Create the stack and store the token. This is the only place it is
#    typed; the task prompts and hides the input, so it never reaches the
#    shell history. `pass hetzner/token | task cluster:token` also works.
task cluster:token stack=dev

# 3. Bake the Talos snapshot. Once per Talos version; idempotent.
task cluster:image-bake stack=dev

# 4. Build the cluster, reading the diff first.
task cluster:plan stack=dev
task cluster:apply stack=dev

# 5. Point every layer at it, then apply them in order. No token here — the
#    CCM and the CSI driver read the one from step 2, through the same stack
#    reference that carries the kubeconfig.
task platform:init stack=dev ref=<org>/hetzner-cluster/dev
task platform:apply-all stack=dev

# 6. Check what you built.
task cluster:kubeconfig stack=dev
task cluster:status stack=dev
task e2e
```

Nodes stay `NotReady` between steps 4 and 5. That is the handover point, not a
failure: the cluster tier installs no CNI, and `layers/10-node-platform` does.

`task up stack=dev` does steps 4 and 5 in one go, once the stacks exist.

## Prerequisites

| Tool | Why |
|------|-----|
| [Pulumi](https://www.pulumi.com/docs/install/) 3.261+ | runs everything here; `pulumi login` before the first task |
| [Go](https://go.dev/dl/) 1.27+ | the programs are Go |
| [Task](https://taskfile.dev/installation/) 3.53+ | the entry points; the [remote Taskfiles](https://github.com/oleg-tkachuk/taskfiles) it includes need 3.53 |
| [hcloud CLI](https://github.com/hetznercloud/cli) | inspection, and baking the Talos image |
| [hcloud-upload-image](https://github.com/apricote/hcloud-upload-image) | Hetzner has no custom-image upload API |
| [talosctl](https://www.talos.dev/) | day-2: upgrades, etcd snapshots |
| [jq](https://github.com/jqlang/jq) | two JSON field reads in the status tasks |

On macOS, `brew bundle` installs all of it. Read the `talosctl` note in the
[`Brewfile`](Brewfile) first: Homebrew ships a newer minor than the topology
pins, and `task cluster:config-check` declines a mismatched binary rather than
trusting it.

Optional, and only for the tasks that name them: `golangci-lint`, `gitleaks`,
`gosec`, `trivy`, `lefthook`. Each task says what to install rather than
skipping itself silently. Git hooks are opt-in per clone with
`lefthook install`.

## Commands

`task` on its own lists everything. Every cluster and layer task takes
`stack=<name>`, and there is no default — a task that assumed one is a task
that can be aimed at the wrong environment by forgetting a word. It is the
only deployment parameter: where a cluster lives and how it is shaped comes
from its committed topology.

| Task | Does |
|------|------|
| `task up` | cluster, then every layer in dependency order |
| `task plan` | preview the cluster and every layer; change nothing |
| `task cluster:status` | nodes, then anything not Running |
| `task platform:status` | which layers are deployed |
| `task e2e` | verify a running cluster; read-only |
| `task verify` | everything checkable without a cluster |
| `task scan` | every scanner CI runs |

Full reference: [commands.md](docs/commands.md).

## Configuration

Everything that shapes a cluster is in `infra/cluster/cluster.<stack>.yaml`,
copied from [cluster.example.yaml](infra/cluster/cluster.example.yaml) and
validated as you type it. Stack config holds the Hetzner token and what each
layer deploys:

| Key | Where | Meaning |
|-----|-------|---------|
| `hcloud:token` | [`infra/cluster`](infra/cluster) | Hetzner API token (secret) |
| `<layer>:clusterStackRef` | every [layer](layers) | `<org>/hetzner-cluster/<stack>` |
| `cluster-services:acmeEmail` | [`30-cluster-services`](layers/30-cluster-services) | enables the Let's Encrypt ClusterIssuer; omit it and none is created |
| `ingress:loadBalancerType` | [`40-ingress`](layers/40-ingress) | Hetzner load balancer type, default `lb11` |
| `gitops:domain` | [`50-gitops`](layers/50-gitops) | publishes Argo CD through ingress; omit it and there is no Ingress |

Details, including why the token is committed encrypted and how to keep state
at Hetzner instead: [configuration.md](docs/configuration.md).

## Documentation

| Document | Covers |
|----------|--------|
| [design.md](docs/design.md) | why it is shaped this way: layers, the committed topology, encryption, version pinning |
| [configuration.md](docs/configuration.md) | the topology file, stack config, state and secrets |
| [commands.md](docs/commands.md) | every task, as tables |
| [operations.md](docs/operations.md) | running a cluster that exists: stopping, starting, the checks, kubectl |
| [ci.md](docs/ci.md) | how changes land, the scanners, what the suites prove, chart upgrades |
| [SECURITY.md](.github/SECURITY.md) | reporting a vulnerability, and what is in scope |

## Layout

| Path | Holds |
|------|-------|
| [infra/cluster/](infra/cluster) | the Hetzner cluster: network, firewall, control plane, workers |
| [layers/](layers) | one Pulumi project per platform layer |
| [pkg/hetzner/](pkg/hetzner) | cluster component resources and topology validation |
| [pkg/layer/](pkg/layer) | the shim every layer shares: cluster resolution, provider, Helm |
| [pkg/charts/](pkg/charts) | every chart version, pinned |
| [pkg/clusterref/](pkg/clusterref) | the output contract between the cluster tier and the layers |
| [pkg/values/](pkg/values) | every chart's Helm values, as templates |
| [tools/](tools) | the checks: chart pins, topology, orphaned resources, stack state |
| [test/e2e/](test/e2e) | verification against a running cluster |
| [tasks/](tasks) | task definitions |
| [docs/](docs) | the documents above |

## Status

Built and running on a single-control-plane dev cluster, on encrypted system
volumes. Five layers: the node platform, Cilium network policy, core services,
Traefik ingress and Argo CD.

Not done, and blocked on decisions rather than work: no `Ingress` or
`ClusterIssuer`, so nothing is reachable from outside and no certificate is
issued; Argo CD is deployed but reconciles nothing; the network policies are
applied but the default deny is off, waiting for flows that do not exist yet;
etcd snapshots are manual. Observability is deployed through Argo CD rather
than from here.

## License

MIT — see [LICENSE](LICENSE).
