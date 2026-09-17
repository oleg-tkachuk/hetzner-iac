# hetzner-iac

[![ci](https://github.com/oleg-tkachuk/hetzner-iac/actions/workflows/ci.yaml/badge.svg?branch=main)](https://github.com/oleg-tkachuk/hetzner-iac/actions/workflows/ci.yaml)
[![release](https://img.shields.io/github/v/release/oleg-tkachuk/hetzner-iac?sort=semver&label=release&cacheSeconds=3600)](https://github.com/oleg-tkachuk/hetzner-iac/releases/latest)
[![go](https://img.shields.io/github/go-mod/go-version/oleg-tkachuk/hetzner-iac?logo=go&logoColor=white&label=go&cacheSeconds=3600)](go.mod)
[![license: MIT](https://img.shields.io/github/license/oleg-tkachuk/hetzner-iac?label=license&cacheSeconds=3600)](LICENSE)

[![Pulumi](https://img.shields.io/badge/Pulumi-8A3391?logo=pulumi&logoColor=white)](https://www.pulumi.com)
[![Talos](https://img.shields.io/badge/Talos%20Linux-FF7300?logo=talos&logoColor=white)](https://docs.siderolabs.com/talos)
[![Cilium](https://img.shields.io/badge/Cilium-F8C517?logo=cilium&logoColor=black)](https://cilium.io)
[![Argo CD](https://img.shields.io/badge/Argo%20CD-EF7B4D?logo=argo&logoColor=white)](https://argo-cd.readthedocs.io)
[![Hetzner Cloud](https://img.shields.io/badge/Hetzner%20Cloud-D50C2D?logo=hetzner&logoColor=white)](https://www.hetzner.com/cloud)

Kubernetes on [Hetzner Cloud](https://www.hetzner.com/cloud), running
[Talos Linux](https://www.siderolabs.com/talos-linux) and built with the
[Pulumi Go SDK](https://www.pulumi.com/docs/iac/languages-sdks/go/).

[Hetzner](https://www.hetzner.com/) has no managed Kubernetes, so this
repository builds the cluster itself — a Talos control plane on a private
network — and then deploys the platform onto it in independent, idempotent
layers.

```
infra/cluster              the only project that talks to the Hetzner API
  └─ exports kubeconfig ──► layers/10-node-platform       Cilium, hcloud CCM + CSI
                            layers/20-network-policy      Cilium network policy (opt-in)
                            layers/30-cluster-services    cert-manager, ESO, metrics-server
                            layers/40-ingress             Traefik
                            layers/50-gitops              Argo CD
                            layers/60-backup              Storage Box for etcd snapshots
```

## Contents

- [Quick start](#quick-start) — from nothing to a running platform
- [Getting back in](#getting-back-in) — a new machine, or a new console
- [Prerequisites](#prerequisites) — what has to be installed
- [Commands](#commands) — the handful worth knowing
- [Configuration](#configuration) — the topology file and stack config
- [Documentation](#documentation) — the rest, by document
- [Layout](#layout) — where things live in the tree
- [License](#license)

## Quick start

You need three things: a [Hetzner Cloud API](https://docs.hetzner.cloud/reference/cloud) token with read+write scope
([how to create one](https://docs.hetzner.com/cloud/api/getting-started/generating-api-token/)), a
[Pulumi Cloud](https://app.pulumi.com/signup) account for state — free for an
individual — and the tools in [Prerequisites](#prerequisites). State can live
in your own S3 bucket instead, which changes step 1 and nothing else:
[configuration.md](docs/configuration.md#keeping-state-in-your-own-s3-bucket).

A **domain** is the fourth thing, and it is needed only for the step none of
the commands below is: reaching the cluster from outside. `40-ingress` creates
the DNS records and `30-cluster-services` orders the certificate, both from
`metadata.domain` in the topology — `platform.example.com`, with
`metadata.dnsZone: example.com` when Hetzner serves that zone. A domain hosted
anywhere else works the same way: leave `metadata.dnsZone` empty and two
records are yours to write, which is the only difference. Leave it out and every command below still works; the ingress layer writes
no records, `50-gitops` no Ingress, and the Argo CD UI is reached with
`kubectl port-forward`. What the domain has to be, and the order to obtain the
certificate in: [domain.md](docs/domain.md).

Everything below creates **billable**
[Hetzner resources](https://www.hetzner.com/cloud#pricing);
`task destroy` removes them — every layer, then the cluster.

```bash
# 1. The repository, and the backend that will hold the state. Bare
#    `pulumi login` is Pulumi Cloud; an S3 bucket is the same command with a
#    URL, and then PULUMI_CONFIG_PASSPHRASE has to be in the environment.
git clone https://github.com/oleg-tkachuk/hetzner-iac.git && cd hetzner-iac
pulumi login

# 2. Describe the cluster. Set network.adminCIDRs to the address you apply
#    from: Talos configuration goes over the Talos API, and a host outside
#    that list hangs with the port filtered.
cp infra/cluster/cluster.example.yaml infra/cluster/cluster.dev.yaml
$EDITOR infra/cluster/cluster.dev.yaml

# 3. Create the stack and store the token, encrypted. Typed once: every
#    task and both Pulumi tiers read it back from here. Never as a task
#    argument — argv is visible to `ps` and lands in the shell history.
task cluster:token stack=dev

# 4. Bake the Talos snapshot. Once per Talos version; idempotent.
task cluster:image:bake stack=dev

# 5. Build the cluster, reading the diff first.
task cluster:plan stack=dev
task cluster:apply stack=dev

# 6. Point every layer at it, then apply them in order. No token here — the
#    CCM and the CSI driver read the one from step 3, through the same stack
#    reference that carries the kubeconfig.
task platform:init stack=dev
task platform:apply layer=all stack=dev

# 7. Check what you built.
task cluster:kubeconfig stack=dev
task cluster:status stack=dev
task e2e
```

Step 3 prompts and hides the input. It reads standard input too, so a secret
manager can supply it without the value ever appearing in a terminal:

```bash
pass hetzner/token | task cluster:token stack=dev
```

The stack is required in both forms — without it the task prints its usage and
never reads stdin, which reads as the pipe having failed.

The token lands in `infra/cluster/Pulumi.dev.yaml` as ciphertext, encrypted by
that stack's secrets provider, and that file is gitignored — so no plaintext
copy exists anywhere. The task is a wrapper around `pulumi config set --secret
hcloud:token` run in `infra/cluster`, which is the same thing by hand. Only the
cluster tier holds a token; the layers reach it through the stack reference. An
exported `HCLOUD_TOKEN` wins over the stored one, which is how CI and a shell
already holding a token for another project keep working.

Nodes stay `NotReady` between steps 5 and 6. That is the handover point, not a
failure: the cluster tier installs no CNI, and `layers/10-node-platform` does.

`task up stack=dev` does steps 5 and 6 in one go, once the stacks exist.

## Getting back in

A new machine, or a new console, needs nothing from the old shell. Everything
about the cluster is in the Pulumi stack: its state, the Hetzner token
encrypted in its config, the kubeconfig, the talosconfig and the Talos secrets
bundle. `./kubeconfig` and `./talosconfig` are gitignored working copies, and
both are written from the stack rather than kept.

```bash
# 1. The same first step as above: the repository, and the backend that holds
#    the state. Nothing is created here — the stack already exists.
git clone https://github.com/oleg-tkachuk/hetzner-iac.git && cd hetzner-iac
pulumi login

# 2. What stacks exist, and which of them this clone can describe. First,
#    because it reports a stack whose topology file is missing here rather
#    than failing three commands later.
task cluster:stacks

# 3. The credentials, out of the stack. No token, no topology file and no
#    network path to the cluster: this reads the backend, not the servers.
task cluster:kubeconfig stack=dev
task cluster:talosconfig stack=dev

# 4. Check they answer.
task cluster:status stack=dev
```

If the state is in your own S3 bucket rather than Pulumi Cloud, step 1 is
`pulumi login 's3://…'` and the secrets need the passphrase that encrypted
them — `export PULUMI_CONFIG_PASSPHRASE=…` before step 3, or `pulumi stack
output` cannot decrypt:
[configuration.md](docs/configuration.md#keeping-state-in-your-own-s3-bucket).

**Step 4 hangs if the address you are on is not in `network.adminCIDRs`.** The
firewall opens the Kubernetes API and the Talos API to those CIDRs and to
nothing else, so a console on a different network reaches neither — the
credentials are right and the packets never arrive. That is the one case that
needs the topology file back, and `infra/cluster/cluster.<stack>.yaml` is
gitignored: it names the networks you administer from, and this repository is
public. So it comes from wherever you kept it.

```bash
# Restore the topology, add the address, then read the diff before applying.
$EDITOR infra/cluster/cluster.dev.yaml
task cluster:plan stack=dev     # the only change you want is the firewall
task cluster:apply stack=dev
```

Why the plan and not just the apply: a topology rebuilt from
`cluster.example.yaml` instead of restored can differ from what the cluster
was built with, and a value that forces a replacement replaces a server.
[operations.md](docs/operations.md#from-a-machine-the-firewall-does-not-know)
has the rest, including what to do when the address is not a stable one.

## Prerequisites

What building and running a cluster needs. The tools the checks use — linters,
scanners, chart rendering, link checking — are not here: CI installs them, and
a clone needs them only to run the same checks locally.
[ci.md](docs/ci.md#tools-the-checks-need) lists those.

| Tool | Why |
|------|-----|
| [Pulumi](https://www.pulumi.com/docs/install/) 3.261+ | runs everything here; `pulumi login` before the first task |
| [Go](https://go.dev/dl/) 1.27+ | the programs are Go |
| [Task](https://taskfile.dev/installation/) 3.53+ | the entry points; the [remote Taskfiles](https://github.com/oleg-tkachuk/taskfiles) it includes need 3.53 |
| [hcloud CLI](https://github.com/hetznercloud/cli) | inspection, and baking the Talos image |
| [hcloud-upload-image](https://github.com/apricote/hcloud-upload-image) | Hetzner has no custom-image upload API |
| [talosctl](https://docs.siderolabs.com/talos/v1.13/getting-started/talosctl) | validates the machine config before anything exists; then upgrades, etcd snapshots, clean shutdown |
| [kubectl](https://kubernetes.io/docs/tasks/tools/) | the status tasks, `task cluster:kubeconfig:add` and `task cluster:orphans` |
| [jq](https://github.com/jqlang/jq) | reads single fields out of `pulumi stack output --json` and `hcloud -o json` |

On macOS, `brew bundle` installs all of it but one: `hcloud-upload-image` is
not in Homebrew, so `go install github.com/apricote/hcloud-upload-image@latest`
— the [Brewfile](Brewfile) says the same at the bottom, and
`task cluster:image:bake` refuses to start without it.

One extra step for `talosctl`:
Homebrew carries only the newest, which is a minor ahead of what the topology
pins, and `task cluster:machine-config:check` declines a mismatched binary
rather than trusting it. Run

    task cluster:talosctl:install

once — it needs no stack and works on a fresh clone, because it reads the
version from the committed topologies. It writes that version into `bin/`,
which the check prefers, so Homebrew's copy can stay on PATH for everything
else.

Three more for one task each. `task cluster:etcd:upload` needs `restic`, which
does the upload, the retention and the integrity check, and `rclone`, which is
only its transport — restic's own sftp backend speaks key authentication and a
Storage Box credential is a generated password. `task cluster:hubble` needs
[`hubble`](https://github.com/cilium/hubble), the CLI that reads the flows
`layers/20-network-policy` is built from.

Every task says what to install rather than skipping itself silently, so a
clone missing one of these is not a broken clone.

## Commands

`task` on its own lists every command for operating a cluster. Each cluster
and layer task takes `stack=<name>`, and there is no default — a task that
assumed one is a task that can be aimed at the wrong environment by forgetting
a word. It is the only deployment parameter: where a cluster lives and how it
is shaped comes from its topology file.

| Task | Does |
|------|------|
| `task up` | cluster, then every layer in dependency order |
| `task plan` | preview the cluster and every layer; change nothing |
| `task cluster:status` | nodes, then anything not Running |
| `task platform:status` | which layers are deployed |
| `task e2e` | verify a running cluster; read-only |

Full reference: [commands.md](docs/commands.md). The checks and scanners are a
separate entry point, `Taskfile.dev.yaml`, and are for working on this
repository rather than running it: [commands.md](docs/commands.md#working-on-this-repository).

## Configuration

Everything that shapes a cluster is in `infra/cluster/cluster.<stack>.yaml`,
copied from [cluster.example.yaml](infra/cluster/cluster.example.yaml) and
validated as you type it. Stack config holds the Hetzner token and what each
layer deploys:

| Key | Where | Meaning |
|-----|-------|---------|
| `hcloud:token` | [`infra/cluster`](infra/cluster) | Hetzner API token (secret) |
| `<layer>:clusterStackRef` | every [layer](layers) | `<org>/hetzner-cluster/<stack>`; written by `platform:init` |
| `node-platform:cni` | [`10-node-platform`](layers/10-node-platform) | which CNI to install, default `cilium` |
| `network-policy:enabled` | [`20-network-policy`](layers/20-network-policy) | create the policies; off by default, because the first one to select an endpoint denies what it does not name |
| `cluster-services:acmeEmail` | [`30-cluster-services`](layers/30-cluster-services) | enables the Let's Encrypt ClusterIssuer; omit it and none is created |
| `cluster-services:acmeStaging` | [`30-cluster-services`](layers/30-cluster-services) | order from Let's Encrypt's staging endpoint: untrusted certificates, and where a new domain's first attempt belongs |
| `ingress:loadBalancerType` | [`40-ingress`](layers/40-ingress) | [Hetzner load balancer](https://www.hetzner.com/cloud/load-balancer) type, default `lb11` |
| `backup:storageBoxType` | [`60-backup`](layers/60-backup) | [Storage Box](https://www.hetzner.com/storage/storage-box) type, default `bx11` |

Argo CD's hostname is **not** stack config, and neither is the cluster's
domain: both come from `metadata.domain` in the topology, so `40-ingress` and
`50-gitops` cannot spell it differently. It is a prerequisite of being
reachable rather than of installing —
[domain.md](docs/domain.md) has the two fields, the delegation and the
staging-first order for the certificate.

Details, including where the encrypted token lives and how to keep state
at Hetzner instead: [configuration.md](docs/configuration.md).

## Documentation

| Document | Covers |
|----------|--------|
| [design.md](docs/design.md) | why it is shaped this way: layers, the committed topology, encryption, version pinning |
| [networking.md](docs/networking.md) | how a request reaches a pod, how pod traffic crosses nodes, and the default deny |
| [domain.md](docs/domain.md) | what being reachable from outside needs, and in what order |
| [configuration.md](docs/configuration.md) | the topology file, stack config, state and secrets |
| [commands.md](docs/commands.md) | every task, as tables |
| [operations.md](docs/operations.md) | running a cluster that exists: stopping, starting, the checks, kubectl |
| [recovery.md](docs/recovery.md) | upgrades, etcd snapshots and restoring from one |
| [ci.md](docs/ci.md) | how changes land, the scanners, what the suites prove, chart upgrades |
| [ROADMAP.md](ROADMAP.md) | what is intended next, and what is deliberately not planned |
| [adr/](docs/adr/) | the decisions this is built on: what was decided, and the consequences accepted |
| [SECURITY.md](.github/SECURITY.md) | reporting a vulnerability, and what is in scope |

## Layout

| Path | Holds |
|------|-------|
| [infra/cluster/](infra/cluster) | the Hetzner cluster: network, firewall, control plane, workers |
| [layers/](layers) | one Pulumi project per platform layer |
| [internal/pkg/](internal/pkg) | the implementation: Pulumi components, the output contract, chart pins, values |
| [internal/ci/](internal/ci) | the repository's own gates: the cross-file contracts nothing else compares |
| [tools/](tools) | the small commands: chart pins, topology, orphaned resources, stack state, snapshot and secrets |
| [test/e2e/](test/e2e) | verification against a running cluster |
| [tasks/](tasks) | the cluster and layer task definitions the root Taskfile includes |
| [Taskfile.dev.yaml](Taskfile.dev.yaml) | the second entry point: the checks, the scanners, the formatters and the chart pins |
| [docs/](docs) | the documents above |

### Not a Go library

`go get` on this module does not resolve, and that is not a defect to report.
The module path carries no `/vN` suffix while the release tags are at v2 and
above, which Go requires for a module meant to be imported — and nothing here
is meant to be. Everything outside `internal/` is a `main` package, and Go
itself refuses an import of `internal/` from another module.

The tags exist for the repository, not for a consumer: they are what
semantic-release writes release notes against. Clone it and run the tasks.

## License

MIT — see [LICENSE](LICENSE).
