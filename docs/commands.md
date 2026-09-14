# Command reference

`task` on its own lists everything. Every cluster and layer task takes
`stack=<name>`, and there is no default. A task that assumed one is a task
that can be aimed at the wrong environment by forgetting a word, so running
one without it prints the usage instead:

    $ task platform:plan-all
    task: platform:plan-all needs a stack, and there is no default.

        task platform:plan-all stack=dev

There is no default because an empty one is not an error. `pulumi --stack ""`
ignores the empty value and uses the stack selected in the workspace — local,
invisible state left by the last `pulumi stack select` — so without the guard a
forgotten word aims the command at whatever that happens to be rather than
failing.

It is the only deployment parameter: where a cluster lives and how it is
shaped comes from its committed topology file.

`task platform:plan` and its siblings also need `layer=`, and that one is
checked against the list of layers rather than merely for being present — so
a typo is caught before anything runs:

    $ task platform:plan stack=dev layer=30-cor
    task: ... layer has an invalid value : '30-cor'
      (allowed values : [10-node-platform 20-network-policy 30-cluster-services 40-ingress 50-gitops])

`task platform:init` needs no reference: it reads the cluster tier's stack name
from `infra/cluster` and writes that into every layer. `ref=` overrides it, for
a cluster in another organization or one shared by several layer stacks.

Every task that runs `pulumi up` or `pulumi destroy` asks before it does —
applying is not the safe half of the pair, because `pulumi up` replaces a
resource for any input that forces a replacement, and replacing the only
control-plane server takes the cluster down. `--yes` skips the question, which
is what a script should have to say out loud:

    $ task platform:apply-all stack=dev
    Apply every platform layer on dev? [y/N]

`task up` asks twice rather than three times — once for the servers, once for
everything on them — because Task prompts per task it runs and `up` adds none
of its own.

Tasks from the shared library
([oleg-tkachuk/taskfiles](https://github.com/oleg-tkachuk/taskfiles), pinned)
are trimmed with `excludes:` to what works here. Four modules are included:
`go`, `security`, `helm` and `hcloud` — the last one is where every
`hcloud:` task below comes from. A module task that cannot
succeed in this repository is worse than a missing one: it is a command
someone runs once, in an emergency, and gets a confusing failure from.

## Whole platform

| Task | Does |
|------|------|
| `task up` | cluster, then every layer in dependency order; asks twice |
| `task plan` | preview the cluster and every layer; change nothing |
| `task build` | compile every program into `bin/` |
| `task verify` | everything checkable without a cluster — needs helm, talosctl and docker |
| `task scan` | every scanner CI runs — gitleaks, trivy, govulncheck, gosec, checkov |
| `task e2e` | verify a running cluster; read-only |
| `task fmt` | format and tidy |
| `task fmt-check` | fail if `gofmt -s` would change anything; the library's gate, and what CI runs |
| `task clean` | remove build output: `bin/` and the layer binaries under `.cache` |

## Cluster

| Task | Does |
|------|------|
| `task cluster:image-bake` | bake the Talos snapshot named by the topology, per version **and** architecture; idempotent |
| `task cluster:init` | create the Pulumi stack for this environment |
| `task cluster:token` | store the Hetzner token in the stack, encrypted; prompts, or reads stdin |
| `task cluster:plan` | show what applying would change |
| `task cluster:apply` | provision or converge the cluster; asks first |
| `task cluster:destroy` | delete the servers; asks first. Keeps the cluster CA, which is protected |
| `task cluster:destroy-secrets` | delete the cluster CA as well; unrecoverable |
| `task cluster:kubeconfig` | write `./kubeconfig` |
| `task cluster:smoke` | ask whether the cluster can run a workload — nodes, a volume, a load balancer |
| `task cluster:kubeconfig-add` | add this cluster to `~/.kube/config`, so a plain `kubectl` reaches it |
| `task cluster:talosconfig` | write `./talosconfig` |
| `task cluster:outputs` | stack outputs, secrets redacted |
| `task cluster:nodes` | list nodes |
| `task cluster:status` | nodes, then anything not Running |
| `task cluster:hubble` | print recent pod flows through Hubble; `last=<n>` to widen |
| `task cluster:config-check` | Talos accepts the machine-config patches |
| `task cluster:encryption-check` | the system volumes are really encrypted, not just configured to be |
| `task cluster:orphans` | Hetzner resources nothing in the cluster claims; read-only |
| `task cluster:stop` | bring the cluster down cleanly through Talos; the instances keep existing |
| `task cluster:reboot` | reboot the nodes through Talos; they come back by themselves |
| `task cluster:etcd-snapshot` | snapshot etcd into `.backups/` |
| `task cluster:secrets-export` | print the Talos secrets bundle — pipe it into a password store |
| `task cluster:etcd-restore` | restore etcd from `snapshot=<path>`; wipes every control-plane node first, asks first |
| `task cluster:upgrade-talos` | upgrade Talos, one node at a time |
| `task cluster:upgrade-k8s` | upgrade Kubernetes in place |

### Policy

| Task | Does |
|------|------|
| `task policy:check` | run the CrossGuard pack over the cluster tier and every layer; changes nothing |
| `task policy:cluster` | run it over the cluster tier only |
| `task policy:layer` | run it over one layer — `layer=40-ingress` |

Why a policy pack when the components validate: see
[configuration.md](configuration.md#what-the-policy-pack-enforces).

## Hetzner instances

From the shared library's `hcloud` module, not this repository. `console` takes
`HCLOUD_SERVER=<name>`; the rest act on every server of the cluster.

| Task | Does |
|------|------|
| `task hcloud:servers` | power state of every server in this cluster; read-only |
| `task hcloud:poweron` | power the instances on |
| `task hcloud:shutdown` | ACPI shutdown — the power button, which Talos acts on |
| `task hcloud:poweroff` | cut power, for when Talos cannot answer |
| `task hcloud:reboot` | ACPI reboot — for a kernel that lives when apid does not |
| `task hcloud:reset` | hard reset: a power cut and a start in one |
| `task hcloud:console` | VNC console on one node, `HCLOUD_SERVER=<name>`; the only way to watch a node that will not boot |

## Layers

| Task | Does |
|------|------|
| `task platform:init` | create every layer's stack and point it at the cluster |
| `task platform:plan-all` | preview every layer in order |
| `task platform:apply-all` | apply every layer in dependency order; asks first |
| `task platform:destroy-all` | destroy every layer, in reverse |
| `task platform:plan layer=10-node-platform` | preview one layer |
| `task platform:apply layer=10-node-platform` | apply one layer; asks first |
| `task platform:destroy layer=50-gitops` | destroy one layer |
| `task platform:outputs layer=50-gitops` | one layer's stack outputs |
| `task platform:status` | which layers are deployed, and how large |
| `task platform:layers` | the layer order; CI derives its matrix from this |
| `task helm:list` | every Helm release on the cluster |

## Charts

| Task | Does |
|------|------|
| `task charts:list` | every pinned chart |
| `task charts:outdated` | each pin against the latest upstream chart |
| `task charts:validate` | pins are exact versions, not floating tags |
| `task charts:appversions` | each `AppVersion` is what the pinned chart ships |
| `task charts:render-check` | the charts still produce the workloads and honour the values |

## Code

| Task | Does |
|------|------|
| `task go:test` | the unit suite |
| `task go:test:coverage` | unit suite with an HTML coverage report |
| `task go:test:tagged:compile` | type-check the `e2e` suite, which the default run never compiles |
| `task go:lint` | golangci-lint |
| `task go:vuln` | govulncheck |
| `task go:compile` | type-check without writing a binary |
| `task go:fmt` / `task go:tidy` | format; tidy the module |
| `task go:fmt:check` | fail if `gofmt -s` would change anything; what `task fmt-check` runs |
| `task go:deps:outdated` / `task go:deps:update` | dependency reports and bumps |

## Security

| Task | Does |
|------|------|
| `task security:all` | secrets, filesystem, Go vuln, lint and SAST — what `task scan` runs |
| `task security:secrets` | gitleaks over the whole history |
| `task security:trivy` | vulnerable dependencies and secrets, plus IaC misconfig |
| `task security:gosec` | insecure patterns the compiler is happy with |
| `task checkov` | hardening rules over the manifests and workflows this repository ships |
| `task security:vuln` / `task security:lint` | govulncheck and golangci-lint across every module |

## Testing

| Task | Does |
|------|------|
| `task go:test` | the unit suite |
| `task e2e` | verify a running cluster; read-only, needs `./kubeconfig` |

What each suite proves, and why e2e does not run in CI:
[ci.md](ci.md#what-the-suites-prove).

## Running it day to day

Stopping and starting, reaching the cluster with a plain `kubectl`, the checks
worth running and where the wrappers stop: [operations.md](operations.md).
