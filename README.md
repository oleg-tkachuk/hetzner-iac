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
                            layers/30-core                cert-manager, ESO, metrics-server
                            layers/40-ingress             Traefik      
                            layers/50-gitops              Argo CD
                            layers/60-observability       Prometheus, Grafana, Loki, Tempo, Alloy
```

## Quick start

You need three things: a Hetzner Cloud API token with read+write scope, a
[Pulumi Cloud](https://app.pulumi.com/signup) account for state — free for an
individual, and `pulumi login` is how you get one on this machine — and the
tools in [Prerequisites](#prerequisites). State can live in your own S3 bucket
instead: [docs/configuration.md](docs/configuration.md#keeping-state-in-your-own-s3-bucket).

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
| [Task](https://taskfile.dev/installation/) 3.53+ | the entry points; remote Taskfiles need 3.53 |
| [hcloud CLI](https://github.com/hetznercloud/cli) | inspection, and baking the Talos image |
| [hcloud-upload-image](https://github.com/apricote/hcloud-upload-image) | Hetzner has no custom-image upload API |
| [talosctl](https://www.talos.dev/) | day-2: upgrades, etcd snapshots |
| `jq` | two JSON field reads in the status tasks |

On macOS, `brew bundle` installs all of it. Read the `talosctl` note in the
`Brewfile` first: Homebrew ships a newer minor than the topology pins, and
`task cluster:config-check` declines a mismatched binary rather than trusting
it.

Optional, and only for the tasks that name them: `golangci-lint`, `gitleaks`,
`gosec`, `trivy`, `lefthook`. Each task says what to install rather than
skipping itself silently. Git hooks are opt-in per clone with
`lefthook install`.

## Commands

`task` on its own lists everything. Every cluster and layer task takes
`stack=<name>`, defaulting to `dev` — the only deployment parameter, because
where a cluster lives and how it is shaped comes from its committed topology.

| Task | Does |
|------|------|
| `task up` | cluster, then every layer in dependency order |
| `task plan` | preview the cluster and every layer; change nothing |
| `task cluster:status` | nodes, then anything not Running |
| `task platform:status` | which layers are deployed |
| `task e2e` | verify a running cluster; read-only |
| `task verify` | everything checkable without a cluster |
| `task scan` | every scanner CI runs |

Full reference: [docs/commands.md](docs/commands.md).

## Configuration

Everything that shapes a cluster is in `infra/cluster/cluster.<stack>.yaml`,
validated as you type it. Stack config holds the Hetzner token and what each
layer deploys:

| Key | Where | Meaning |
|-----|-------|---------|
| `hcloud:token` | `infra/cluster` | Hetzner API token (secret) |
| `<layer>:clusterStackRef` | every layer | `<org>/hetzner-cluster/<stack>` |
| `core:acmeEmail` | `30-core` | enables the Let's Encrypt ClusterIssuer; omit it and none is created |
| `ingress:loadBalancerType` | `40-ingress` | Hetzner load balancer type, default `lb11` |
| `gitops:domain` | `50-gitops` | publishes Argo CD through ingress; omit it and there is no Ingress |
| `observability:metricsRetention` | `60-observability` | default `30d` |
| `observability:metricsVolumeSize` | `60-observability` | default `50Gi` |

Details, including why the token is committed encrypted and how to keep state
at Hetzner instead: [docs/configuration.md](docs/configuration.md).

## Documentation

| | |
|---|---|
| [docs/design.md](docs/design.md) | why it is shaped this way — layers, the committed topology, version pinning, what a run prints |
| [docs/configuration.md](docs/configuration.md) | the topology file, stack config, state and secrets |
| [docs/commands.md](docs/commands.md) | every task, and how to test |
| [docs/ci.md](docs/ci.md) | how changes land, the scanners, chart upgrades, why the pipeline is fast |
| [.github/SECURITY.md](.github/SECURITY.md) | reporting a vulnerability, and what is in scope |

## Layout

```
infra/cluster/    the Hetzner cluster: network, firewall, control plane, workers
layers/           one Pulumi project per platform layer
pkg/hetzner/      cluster component resources and topology validation
pkg/layer/        the shim every layer shares: cluster resolution, provider, Helm
pkg/charts/       every chart version, pinned
pkg/clusterref/   the output contract between the cluster tier and the layers
tools/            chart pin auditor, topology validator, and the other checks
test/e2e/         verification against a running cluster
tasks/            task definitions
docs/             the documents above
```

## Status

Built and running on a single-control-plane dev cluster. Not yet done, and
blocked on decisions rather than work: no `Ingress` or `ClusterIssuer`, so
nothing is reachable from outside and no certificate is issued; Argo CD is
deployed but reconciles nothing; Alertmanager has no receiver, and says so on
every apply; etcd snapshots are manual.

## License

MIT — see [LICENSE](LICENSE).
