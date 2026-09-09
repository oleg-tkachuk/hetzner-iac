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

Production Kubernetes on Hetzner Cloud, built with Pulumi and Go.

Hetzner has no managed Kubernetes, so this repository builds the cluster
itself — a Talos control plane on a private network — and then deploys the
platform onto it in independent, idempotent layers.

```
infra/cluster              the only project that talks to the Hetzner API
  └─ exports kubeconfig ──► layers/10-cloud-integration   hcloud CCM + CSI
                            layers/20-cni                 Cilium
                            layers/30-core                cert-manager, ESO, metrics-server
                            layers/40-ingress             ingress-nginx
                            layers/50-gitops              Argo CD
                            layers/60-observability       Prometheus, Grafana, Loki, Tempo, Alloy
```

## Contents

- [Why it is shaped this way](#why-it-is-shaped-this-way)
- [Prerequisites](#prerequisites)
- [Bring-up](#bring-up)
- [Commands](#commands)
  - [Whole platform](#whole-platform)
  - [Cluster](#cluster)
  - [Layers](#layers)
  - [Code](#code)
  - [Security](#security)
  - [Charts](#charts)
- [Configuration](#configuration)
- [Security scanning](#security-scanning)
- [Testing](#testing)
- [Layout](#layout)
- [License](#license)

## Why it is shaped this way

**Each layer is its own Pulumi project.** A layer can be previewed, applied and
destroyed on its own, and reads the cluster's kubeconfig through a
StackReference rather than sharing state with it. Upgrading Cilium does not
mean planning a change to Argo CD.

Independence has a boundary worth stating: layers are independently
*appliable*, not order-free. On an empty cluster nothing schedules before the
CCM clears Talos's `uninitialized` taint, and nothing networks before the CNI.
`task platform:apply-all` walks them in order; the order lives once, in the
Taskfile, and CI derives its matrix from the same list.

**The cluster is a committed file.** `infra/cluster/cluster.<stack>.yaml`
describes the topology, so a cluster is reviewable in a diff before it exists
and reproducible from a clone. It is sparse — anything omitted keeps the
default in `pkg/hetzner` — and it is validated against the same code the Pulumi
program runs, so the check cannot drift from the thing it checks.

**The cluster tier stops at "a Kubernetes API that answers".** It installs no
CNI: Talos would otherwise install Flannel, which would then have to be removed
before Cilium could take over. Nodes are `NotReady` until `20-cni` runs. That
is the handover point, not a failure.

**Every chart version is pinned in one place.** `pkg/charts` is the registry;
floating tags are rejected by validation rather than by convention.
`task charts:outdated` compares each pin against its upstream repository.

## Prerequisites

| Tool | Why |
|------|-----|
| [Pulumi](https://www.pulumi.com/docs/install/) 3.261+ | runs everything here |
| [Go](https://go.dev/dl/) 1.27+ | the programs are Go |
| [Task](https://taskfile.dev/installation/) 3.53+ | the entry points; remote Taskfiles need 3.53 |
| [hcloud CLI](https://github.com/hetznercloud/cli) | inspection, and baking the Talos image |
| [hcloud-upload-image](https://github.com/apricote/hcloud-upload-image) | Hetzner has no custom-image upload API |
| [talosctl](https://www.talos.dev/) | day-2: upgrades, etcd snapshots |
| `jq`, `curl` | used by the image-bake task |

A Hetzner Cloud API token with read+write scope on the project.

Optional, and only for the tasks that name them: `golangci-lint`, `gitleaks`,
`gosec`, `trivy`, `lefthook`. Each task says what to install rather than
skipping itself silently.

Hooks are opt-in per clone:

```bash
lefthook install
```

## Bring-up

```bash
export HCLOUD_TOKEN=...

# 1. Describe the cluster. Set network.adminCIDRs to the address you will
#    apply from — Talos configuration is pushed over the Talos API, and a host
#    outside that list hangs with the port filtered.
$EDITOR infra/cluster/cluster.prod.yaml

# 2. Bake the Talos snapshot. Once per Talos version; idempotent.
task cluster:image-bake stack=prod

# 3. Create the stack and set the token as a secret.
task cluster:init stack=prod
cd infra/cluster && pulumi config set --secret hcloud:token "$HCLOUD_TOKEN" && cd -

# 4. Build the cluster.
task cluster:plan stack=prod      # read the diff first
task cluster:apply stack=prod

# 5. Point every layer at it, then apply them in order.
task platform:init stack=prod ref=<org>/hetzner-cluster/prod
cd layers/10-cloud-integration && \
  pulumi config set --secret cloud-integration:hcloudToken "$HCLOUD_TOKEN" && cd -
task platform:apply-all stack=prod

# 6. Check what you built.
task cluster:kubeconfig stack=prod
task cluster:status stack=prod
task e2e stack=prod
```

`task up stack=prod` does steps 4 and 5 in one go, once the stacks exist.

## Commands

`task` on its own lists everything. Every cluster and layer task takes
`stack=<name>`, defaulting to `prod` — that is the only deployment parameter,
because where a cluster lives and how it is shaped comes from its committed
topology file.

Tasks from the shared library
([oleg-tkachuk/taskfiles](https://github.com/oleg-tkachuk/taskfiles), pinned)
are trimmed with `excludes:` to what works here. A module task that cannot
succeed in this repository is worse than a missing one: it is a command
someone runs once, in an emergency, and gets a confusing failure from.

### Whole platform

| Task | Does |
|------|------|
| `task up` | cluster, then every layer in dependency order |
| `task plan` | preview the cluster and every layer; change nothing |
| `task verify` | the fast gate — format, tests, lint, reachable vulnerabilities, chart pins |
| `task scan` | every scanner CI runs — gitleaks, trivy, govulncheck, gosec |
| `task e2e` | verify a running cluster; read-only, safe against production |
| `task fmt` | format and tidy |
| `task fmt-check` | fail if anything is not gofmt-clean |
| `task clean` | drop the compiled layer binaries under `.cache` |

### Cluster

| Task | Does |
|------|------|
| `task cluster:image-bake` | bake the Talos snapshot named by the topology; idempotent |
| `task cluster:init` | create the Pulumi stack for this environment |
| `task cluster:plan` | show what applying would change |
| `task cluster:apply` | provision or converge the cluster |
| `task cluster:destroy` | delete the servers; asks first |
| `task cluster:kubeconfig` | write `./kubeconfig` |
| `task cluster:talosconfig` | write `./talosconfig` |
| `task cluster:outputs` | stack outputs, secrets redacted |
| `task cluster:nodes` | list nodes |
| `task cluster:status` | nodes, then anything not Running |
| `task cluster:etcd-snapshot` | snapshot etcd into `.backups/` |
| `task cluster:upgrade-talos` | upgrade Talos, one node at a time |
| `task cluster:upgrade-k8s` | upgrade Kubernetes in place |

Upgrading Talos means bumping `talos.version` in the topology, re-running
`cluster:image-bake`, then `cluster:upgrade-talos`. Nodes are upgraded in
place; they are never replaced, which is why the server resource ignores
changes to its image.

### Layers

| Task | Does |
|------|------|
| `task platform:init ref=<org>/hetzner-cluster/<stack>` | create every layer's stack and point it at the cluster |
| `task platform:plan-all` | preview every layer in order |
| `task platform:apply-all` | apply every layer in dependency order |
| `task platform:destroy-all` | destroy every layer, in reverse |
| `task platform:plan layer=20-cni` | preview one layer |
| `task platform:apply layer=20-cni` | apply one layer |
| `task platform:destroy layer=60-observability` | destroy one layer |
| `task platform:outputs layer=50-gitops` | one layer's stack outputs |
| `task platform:status` | which layers are deployed, and how large |
| `task platform:layers` | the layer order; CI derives its matrix from this |
| `task helm:list` | every Helm release on the cluster |

### Code

| Task | Does |
|------|------|
| `task go:test` | the unit suite |
| `task go:test:coverage` | unit suite with an HTML coverage report |
| `task go:test:tagged:compile` | type-check the `e2e` suite, which the default run never compiles |
| `task go:lint` | golangci-lint |
| `task go:vuln` | govulncheck |
| `task go:compile` | type-check without writing a binary |
| `task go:fmt` / `task go:tidy` | format; tidy the module |
| `task go:deps:outdated` / `task go:deps:update` | dependency reports and bumps |

### Security

| Task | Does |
|------|------|
| `task security:all` | secrets, filesystem, Go vuln, lint and SAST — what `task scan` runs |
| `task security:secrets` | gitleaks over the whole history |
| `task security:trivy` | vulnerable dependencies and secrets, plus IaC misconfig |
| `task security:gosec` | insecure patterns the compiler is happy with |
| `task security:vuln` / `task security:lint` | govulncheck and golangci-lint across every module |

### Charts

| Task | Does |
|------|------|
| `task charts:list` | every pinned chart |
| `task charts:outdated` | each pin against the latest upstream chart |
| `task charts:validate` | pins are exact versions, not floating tags |

## Configuration

Cluster shape lives in `infra/cluster/cluster.<stack>.yaml`. Everything else is
Pulumi config:

| Key | Where | Meaning |
|-----|-------|---------|
| `hcloud:token` | `infra/cluster` | Hetzner API token (secret) |
| `hetzner-cluster:publicIPv4` | `infra/cluster` | routable address per node; required unless you apply from inside the private network |
| `hetzner-cluster:allowICMP` | `infra/cluster` | open ping from the admin CIDRs |
| `hetzner-cluster:imageSelector` | `infra/cluster` | override the Talos snapshot selector |
| `<layer>:clusterStackRef` | every layer | `<org>/hetzner-cluster/<stack>` |
| `cloud-integration:hcloudToken` | `10-cloud-integration` | token for the CCM and CSI (secret) |
| `core:acmeEmail` | `30-core` | enables the Let's Encrypt ClusterIssuer; omit it and none is created |
| `ingress:loadBalancerType` | `40-ingress` | Hetzner load balancer type, default `lb11` |
| `gitops:domain` | `50-gitops` | publishes Argo CD through ingress; omit it and there is no Ingress |
| `observability:metricsRetention` | `60-observability` | default `30d` |
| `observability:metricsVolumeSize` | `60-observability` | default `50Gi` |

The Hetzner token is read by the cloud-integration layer rather than exported
by the cluster tier: a stack that exports a cloud credential puts it into the
state of every stack that references it.

## Security scanning

The scanners live in their own workflow, `.github/workflows/security.yaml`,
which CI calls and which also runs weekly on its own. That schedule is the
reason for the split: a CVE published today makes yesterday's green commit
vulnerable, and a gate that only runs on push would never say so.

Each job installs its tool and then calls the same task an operator runs
locally, so the flags live in one place rather than being restated in YAML.
`task scan` is the whole set.

Accepted findings live in `.trivyignore.yaml`, each with the reason it stands.
Entries are removed as soon as a fix lands — a stale ignore masks the finding
coming back.

Note that govulncheck and trivy disagree by design: govulncheck reports only
what this code can actually reach, trivy reports everything present in the
dependency graph. Both are useful, and a finding in one and not the other is
information rather than a contradiction.

## Testing

```bash
task go:test         # unit
task e2e stack=prod  # against a real cluster; read-only, safe in production
```

The unit tests pin what Pulumi will *ask for*, including the settings whose
mismatch never fails an apply — kube-proxy replacement, PROXY protocol on both
sides of the load balancer, the KubePrism port. They exercise the resource
graph under Pulumi's mock monitor, so no cloud account is involved.

The e2e suite checks what actually happened: taints cleared, routes
programmed, the load balancer provisioned, volumes bound. It lives behind the
`e2e` build tag, so `go test ./...` never reaches for a cluster.

## Layout

```
infra/cluster/    the Hetzner cluster: network, firewall, control plane, workers
layers/           one Pulumi project per platform layer
pkg/hetzner/      cluster component resources and topology validation
pkg/layer/        the shim every layer shares: cluster resolution, provider, Helm
pkg/charts/       every chart version, pinned
pkg/clusterref/   the output contract between the cluster tier and the layers
tools/            chart pin auditor, topology validator
test/e2e/         verification against a running cluster
tasks/            task definitions
```

## License

MIT.
