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
      (allowed values : [10-node-platform 20-network-policy 30-core 40-ingress 50-gitops])

`task platform:init` needs `ref=`.

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
are trimmed with `excludes:` to what works here. A module task that cannot
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
| `task cluster:image-bake` | bake the Talos snapshot named by the topology; idempotent |
| `task cluster:init` | create the Pulumi stack for this environment |
| `task cluster:token` | store the Hetzner token in the stack, encrypted; prompts, or reads stdin |
| `task cluster:plan` | show what applying would change |
| `task cluster:apply` | provision or converge the cluster; asks first |
| `task cluster:destroy` | delete the servers; asks first. Keeps the cluster CA, which is protected |
| `task cluster:destroy-secrets` | delete the cluster CA as well; unrecoverable |
| `task cluster:kubeconfig` | write `./kubeconfig` |
| `task cluster:kubeconfig-add` | add this cluster to `~/.kube/config`, so a plain `kubectl` reaches it |
| `task cluster:talosconfig` | write `./talosconfig` |
| `task cluster:outputs` | stack outputs, secrets redacted |
| `task cluster:nodes` | list nodes |
| `task cluster:status` | nodes, then anything not Running |
| `task cluster:hubble` | print recent pod flows through Hubble; `last=<n>` to widen |
| `task cluster:config-check` | Talos accepts the machine-config patches |
| `task cluster:encryption-check` | the system volumes are really encrypted, not just configured to be |
| `task cluster:orphans` | Hetzner resources nothing in the cluster claims; read-only |
| `task cluster:power-status` | power state of every server in this cluster; read-only |
| `task cluster:stop` | clean shutdown through Talos; the servers keep existing |
| `task cluster:start` | power the servers back on through the Hetzner API |
| `task cluster:reboot` | reboot the nodes through Talos |
| `task cluster:power-off` | cut power without a clean shutdown, for when Talos cannot answer |
| `task cluster:etcd-snapshot` | snapshot etcd into `.backups/` |
| `task cluster:upgrade-talos` | upgrade Talos, one node at a time |
| `task cluster:upgrade-k8s` | upgrade Kubernetes in place |

## Layers

| Task | Does |
|------|------|
| `task platform:init ref=<org>/hetzner-cluster/<stack>` | create every layer's stack and point it at the cluster |
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

```bash
task go:test    # unit
task e2e        # against a running cluster; read-only, needs ./kubeconfig
```

The unit tests pin what Pulumi will *ask for*, including the settings whose
mismatch never fails an apply — kube-proxy replacement, PROXY protocol on both
sides of the load balancer, the KubePrism port. They exercise the resource
graph under Pulumi's mock monitor, so no cloud account is involved.

The e2e suite checks what actually happened: taints cleared, routes
programmed, the load balancer provisioned, volumes bound. It lives behind the
`e2e` build tag, so `go test ./...` never reaches for a cluster. It needs
`./kubeconfig`, which `task cluster:kubeconfig` writes.

It runs from the operator's machine rather than from CI: the firewall opens the
Kubernetes API to `network.adminCIDRs` only, and a GitHub-hosted runner is not
in it.

## Stopping and starting

The five power tasks above are wrappers over two APIs, and which API does what
is not a preference:

- **Talos stops a node.** `talosctl shutdown` is a clean shutdown of a machine
  whose entire interface is an API, and on more than one node it can cordon and
  evict first. Hetzner's API can do neither: its `shutdown` is an ACPI signal
  at the guest and its `poweroff` is the plug.
- **Hetzner starts a node.** Nothing else can. A powered-off machine runs no
  apid for `talosctl` to reach, so `poweron` is the only way back.

`cluster:power-off` is separate from `cluster:stop` and named for what it does:
it cuts power mid-write, and etcd recovers on the next boot rather than
starting clean. It is the better option only when Talos cannot answer.

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

The five tasks cover what this repository needs day to day, against every
server of the cluster at once, selected by the `cluster=<name>` label that
`pkg/hetzner` stamps. That label is why they are safe on a shared project and
why they work unchanged on three control planes.

Everything else Hetzner offers is deliberately not wrapped — `hcloud server`
alone has rebuild, change-type, rescue mode, ISO attachment, backups,
snapshots, a VNC console, RDNS and per-server metrics. Use the CLI directly
for those:

```bash
export HCLOUD_TOKEN="$(go run ./tools/token dev)"
hcloud server --help
hcloud server describe platform-dev-control-plane-0
```

A wrapper per API call would be a second, worse CLI to keep in step with the
first. `hcloud server reset` — a hard reboot — is not wrapped for a smaller
reason: it is `power-off` then `start`, and spelling it in two steps makes an
operator notice which half they are in.

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
task cluster:kubeconfig-add stack=dev
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
