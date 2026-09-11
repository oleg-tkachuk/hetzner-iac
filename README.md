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
layers/05-object-storage   buckets that outlive the cluster — no cluster needed

infra/cluster              the only project that talks to the Hetzner API
  └─ exports kubeconfig ──► layers/10-cloud-integration   hcloud CCM + CSI
                            layers/20-cni                 Cilium
                            layers/30-core                cert-manager, ESO, metrics-server
                            layers/40-ingress             ingress-nginx
                            layers/50-gitops              Argo CD
                            layers/60-observability       Prometheus, Grafana, Loki, Tempo, Alloy
```

`05-object-storage` stands apart on purpose. It reads no kubeconfig and holds
what must survive a `pulumi destroy` of everything below it: Pulumi state,
which a cluster's own state cannot contain, and the objects Loki and Tempo
write. Its buckets are created protected and retained on delete, so removing
one takes two deliberate steps.

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
- [How changes land](#how-changes-land)
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
| `task verify` | everything checkable without a cluster — needs helm, talosctl and docker |
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
| `task charts:render-check` | the charts still produce the workloads and honour the values |
| `task cluster:config-check` | Talos accepts the machine-config patches |
| `task observability:check` | Alloy parses the collector config |

## Configuration

State lives in **Pulumi Cloud**, declared as `backend:` in every `Pulumi.yaml`
so it is a property of the repository rather than of whoever last ran
`pulumi login`. To keep state at Hetzner instead, override the URL — it is the
only change needed:

```bash
export PULUMI_BACKEND_URL='s3://<bucket>?endpoint=fsn1.your-objectstorage.com&s3ForcePathStyle=true&region=fsn1'
export AWS_ACCESS_KEY_ID=...
export AWS_SECRET_ACCESS_KEY=...
export PULUMI_CONFIG_PASSPHRASE=...
```

A DIY backend encrypts stack secrets with that passphrase; Pulumi Cloud manages
the key for you. Either way, cloud credentials never enter state — they stay
with the CLI.


Cluster shape lives in `infra/cluster/cluster.<stack>.yaml`. Everything else is
Pulumi config:

| Key | Where | Meaning |
|-----|-------|---------|
| `hcloud:token` | `infra/cluster` | Hetzner API token (secret) |
| `hetzner-cluster:publicIPv4` | `infra/cluster` | routable address per node; required unless you apply from inside the private network |
| `hetzner-cluster:allowICMP` | `infra/cluster` | open ping from the admin CIDRs |
| `hetzner-cluster:imageSelector` | `infra/cluster` | override the Talos snapshot selector |

Both the Talos and the Kubernetes version are pinned in the topology, and
neither derives from the other. An empty `kubernetes.version` takes
`DefaultKubernetesVersion` — also pinned — rather than whatever the configured
Talos release happens to ship, because that made a Talos patch bump able to
move Kubernetes a whole minor with no diff and no decision. It did: the first
bring-up landed on v1.36.0, new enough that `kube-apiserver` had removed a flag
the machine config was passing, and the control plane never started.
| `<layer>:clusterStackRef` | every layer except `05-object-storage` | `<org>/hetzner-cluster/<stack>` |
| `object-storage:location` | `05-object-storage` | `fsn1`, `nbg1` or `hel1` — fewer locations than host servers |
| `object-storage:namePrefix` | `05-object-storage` | prefix for bucket names, which collide across all of Hetzner |
| `object-storage:retainNoncurrentDays` | `05-object-storage` | default `30`; the only lifecycle rule Hetzner implements |
| `object-storage:accessKey` / `:secretKey` | `05-object-storage` | S3 credentials from the Console, not an hcloud token (secret) |
| `cloud-integration:hcloudToken` | `10-cloud-integration` | token for the CCM and CSI (secret) |
| `core:acmeEmail` | `30-core` | enables the Let's Encrypt ClusterIssuer; omit it and none is created |
| `ingress:loadBalancerType` | `40-ingress` | Hetzner load balancer type, default `lb11` |
| `gitops:domain` | `50-gitops` | publishes Argo CD through ingress; omit it and there is no Ingress |
| `observability:metricsRetention` | `60-observability` | default `30d` |
| `observability:metricsVolumeSize` | `60-observability` | default `50Gi` |
| `observability:objectStorageStackRef` | `60-observability` | optional; set it and Loki and Tempo write to the bucket instead of a volume |
| `observability:objectStorageAccessKey` / `:secretKey` | `60-observability` | S3 credentials for that bucket, required with the ref above (secret) |
| `observability:logsRetention` | `60-observability` | default `720h`; what bounds the bucket, which has no size to fill |

The Hetzner token is read by the cloud-integration layer rather than exported
by the cluster tier: a stack that exports a cloud credential puts it into the
state of every stack that references it.

### Chart upgrades arrive as pull requests

Every chart is pinned in [pkg/charts/registry.go](pkg/charts/registry.go) — one
file, no version literal anywhere else. Renovate watches it through a regex in
[.github/renovate.json](.github/renovate.json) and opens one pull request per
chart, weekly, labelled `charts`, with `fix(charts):` so the upgrade reaches a
release (`feat(charts):` for a major, so the version says so).

It runs from [.github/workflows/renovate.yaml](.github/workflows/renovate.yaml)
rather than as the hosted GitHub App, which needed account rights that were not
available. Dependabot was the other option and does not fit: it reads gomod and
github-actions natively but cannot see a chart version pinned inside a Go
source file, which is the whole point here.

Two schedules, which is not a contradiction. The workflow's cron decides how
often Renovate **runs** — daily. `renovate.json` decides what it may **do**
when it runs: regular updates wait for Monday so chart upgrades batch into one
review, while `vulnerabilityAlerts` are exempt and can land any morning. A
weekly cron alone would have delayed a security fix by up to seven days.

`workflow_dispatch` runs a pass now and sets `RENOVATE_FORCE` to ignore the
Monday schedule, which is how the first pass happens without waiting for it.

Self-hosting costs a token. `GITHUB_TOKEN` cannot serve: a pull request opened
with it does not trigger `pull_request` workflows, so no required check would
ever report and branch protection would block the merge for ever. The workflow
fails with that explanation, and the scopes, when `RENOVATE_TOKEN` is unset.

Two things about that are worth knowing before a bot's pull request arrives.

**Renovate cannot maintain `AppVersion`.** The helm datasource knows chart
versions and nothing else, so a bumped pin sits beside an app version the chart
no longer ships. `task charts:appversions` reads each repository's index and
fails when they disagree — a misleading comment for most charts, and a real
defect for alloy, whose validation image tag is built out of it. The pull
request says so in its own body, and the fix is the value that check prints.

**The regex is a silent failure waiting to happen.** It keys off the field order
`Name → Repo → Version`; reorder them and Renovate stops matching, opens no
pull request, and reports nothing. So `tools/charts` reads that regex out of
Renovate's own configuration and asserts it still matches every chart in the
registry. Verified by reordering two fields on purpose:

```
Renovate's pattern does not match chart "loki" (key "loki") — it would never be upgraded
```

### What a run prints

Every layer logs through [pkg/pulumilog](pkg/pulumilog), which borrows its
vocabulary from the [taskfiles](https://github.com/oleg-tkachuk/taskfiles)
repository so that `task` and `pulumi up` read as one tool:

| glyph | means | survives the run |
|-------|-------|------------------|
| `◉` | work starting | no |
| `✔` | work finished | no |
| `○` | deliberately not done | **yes** |
| `▲` | configured, and will not do what it looks like | **yes** |

The last two are the point. A layer that installs cert-manager and no
ClusterIssuer, or Loki on a volume rather than the bucket, is the most
confusing thing this repository can do — so those lines go to Pulumi's
permanent diagnostics and are still on screen when the run ends. Progress lines
are ephemeral, or the summary is one line per release and nobody reads it.

The loudest of them today is Alertmanager. The chart's default route ends at a
receiver named `null`, so alerts are grouped, inhibited and then dropped —
Prometheus stores metrics, rules evaluate, alerts fire, and they reach nobody.
Every apply says so until a receiver exists.

What is *not* wrong, checked rather than assumed: the four scrape targets Talos
does not expose are disabled, and the chart removes their alert rules along
with them. Rendering with and without proves it — `KubeSchedulerDown`,
`KubeControllerManagerDown`, `KubeProxyDown` and `etcdMembersDown` are present
in the chart's default output and absent from ours, and `absent()` drops from
five expressions to one (the API server, which should keep it). There are no
permanently firing alerts to silence.

`NO_COLOR` drops the escape codes and keeps the glyphs. A TTY check would be
wrong rather than merely unhelpful: a Pulumi program's output is captured by
the CLI over gRPC, so stdout is never a terminal.

### Asking the real tool

Four checks run offline against the actual software rather than against this
repository's own assumptions, because that is where the expensive mistakes hide
— each of the following was found this way, and none of them would have failed
a `pulumi up` cleanly:

- Grafana pointed at Tempo's port 3100, which the chart does not expose.
- `machine.network.hostname`, which Talos rejects outright.
- A Talos version pinned ahead of what the provider's generator knows.

`task verify` runs them all. They need `helm`, a `talosctl` matching the pinned
Talos minor, and a running Docker.

## How changes land

Everything goes through a pull request; `main` is protected and takes no direct
pushes.

```
branch → PR → CI → rebase merge → release
```

The pieces that make that work, and the reason each one is there:

- **Rebase is the only merge method.** Each commit of the PR is replayed onto
  `main` as it was written, so the history stays the sequence of changes it
  actually was rather than one squashed lump.
- **Every commit is checked against Conventional Commits.** Under rebase they
  all land on `main`, and semantic-release reads each of them to pick the next
  version and write the notes. A commit that does not conform contributes
  nothing, and a PR made entirely of them produces no release at all —
  silently. CI checks them with the same expression as the local commit-msg
  hook, which is opt-in per clone; the CI check is not.
- **Release is a job of the CI workflow**, gated by `needs:` on every check.
  It used to be a workflow of its own triggered on push, running in parallel
  with the checks — and v1.0.2 was cut from a commit whose CI was failing.
  The commit-message check is deliberately *not* in that `needs:` list: it
  only runs on pull requests, and a skipped dependency would skip the release
  along with it. It is enforced as a required check on the branch instead.
- **`main` requires linear history** and refuses force pushes, so the commit a
  release points at is the commit that was tested.

Release notes are generated from the commit history by semantic-release; there
is no changelog file to keep in step.

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

### What runs when

Pull requests run everything cheap and everything that catches a mistake the
same day. Two checks run nightly instead, because each cost more than ten
minutes of every pipeline for coverage a day's delay does not meaningfully
weaken:

- **the race detector**, which compiles the whole tree a second time with build
  IDs nothing else can reuse;
- **standalone gosec**, because golangci-lint already runs gosec over this code
  and finishes sooner — it loads the package graph once and runs every linter
  over it, where standalone gosec re-loads per package.

`task scan` still runs the full set locally.

A push to `main` runs almost nothing. The pull request has already been through
this exact tree, and branch protection requires the branch to be current with
`main` before merging, so the rebased result is what was tested — re-running it
would cost fifteen minutes and tell nobody anything.

Two things do run there. The release, obviously. And the priming job, which is
less obvious: a cache written on a pull request is scoped to `refs/pull/N/merge`
and no other branch can read it. Only the default branch can seed a cache that
every pull request restores. Skipping `main` entirely would mean every pull
request pays the cold ten-minute build for ever.

That also makes the push to `main` the only run that *writes* the cache. Pull
requests restore it and never save: a 789 MB entry under `refs/pull/N/merge` is
unreadable the moment the branch merges, and three of them were already sitting
against a 10 GB repository limit whose eviction policy is least-recently-used —
which would eventually have taken the `main` entry with them. What a pull
request gives up is one of its own pushes reusing the build of the push before
it; the dependency tree, which is the expensive part, still comes from `main`.

The scanners also gate the expensive half of the pipeline. GitHub has no
job-level fail-fast — a failing job does not stop its siblings — so the priming
job depends on them, and everything expensive depends on priming. A secret, a
reachable vulnerability or a workflow finding therefore stops the run in under a
minute, rather than after ten minutes of compiling.

### Why the pipeline is not slow any more

Every run used to pay for a cold compile of a 208-module graph dominated by the
generated Pulumi Kubernetes SDK. Two separate causes.

**The shared Go cache was poisoned.** `actions/setup-go` caches the module and
build directories under a key derived from `go.sum`, and that key is immutable:
whichever job saves first owns it for good. Early runs failed before they
touched Go, saved a 31 MB cache, and every later run restored those 31 MB and
rebuilt everything. A `prime` job now compiles the tree first, so the cache that
gets saved is the useful one, and the jobs that need it depend on it.

That job builds unconditionally, and the comment above it says why: the first
version skipped the build when no Go had changed, saved an empty cache under
the immutable key, and poisoned it again on the very run that introduced it.

Building unconditionally was still not enough. Every job attached the cache in
both directions, so the *fastest* one owned the key — and the job that compiles
the tree is by definition slower than one that does not. The roles are explicit
now: `.github/actions/setup-go` takes a `cache-mode`, the priming job is the
only writer and only on a push to `main`, everything else is `restore`, and a
race build opts out entirely
because its artifacts carry build IDs nothing else can reuse. CI asserts that
exactly one writer exists, because a comment did not prevent the second
occurrence.

**gosec's memory.** It loads every package with full syntax *and* type
information for the whole transitive graph, and processes `-concurrency` of them
at once — defaulting to the core count, so fourteen large graphs at once on a
developer machine. `GOSEC_FLAGS: -concurrency=4` caps it.

A docs-only change still reports every check: the jobs run and skip their
expensive step, rather than being skipped themselves. A required check that
never reports leaves the pull request waiting forever.

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
