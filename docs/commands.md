# Command reference

`task` on its own lists everything. Every cluster and layer task takes
`stack=<name>`, and there is no default. A task that assumed one is a task
that can be aimed at the wrong environment by forgetting a word, so running
one without it prints the usage instead:

    $ task platform:plan-all
    task: platform:plan-all needs a stack, and there is no default.

        task platform:plan-all stack=dev

It is the only deployment parameter: where a cluster lives and how it is
shaped comes from its committed topology file. `task platform:plan` and its
siblings also need `layer=`, and `task platform:init` needs `ref=`.

Tasks from the shared library
([oleg-tkachuk/taskfiles](https://github.com/oleg-tkachuk/taskfiles), pinned)
are trimmed with `excludes:` to what works here. A module task that cannot
succeed in this repository is worse than a missing one: it is a command
someone runs once, in an emergency, and gets a confusing failure from.

## Whole platform

| Task | Does |
|------|------|
| `task up` | cluster, then every layer in dependency order |
| `task plan` | preview the cluster and every layer; change nothing |
| `task build` | compile every program into `bin/` |
| `task verify` | everything checkable without a cluster — needs helm, talosctl and docker |
| `task scan` | every scanner CI runs — gitleaks, trivy, govulncheck, gosec, checkov |
| `task e2e` | verify a running cluster; read-only |
| `task fmt` | format and tidy |
| `task fmt-check` | fail if anything is not gofmt-clean |
| `task clean` | remove build output: `bin/` and the layer binaries under `.cache` |

## Cluster

| Task | Does |
|------|------|
| `task cluster:image-bake` | bake the Talos snapshot named by the topology; idempotent |
| `task cluster:init` | create the Pulumi stack for this environment |
| `task cluster:token` | store the Hetzner token in the stack, encrypted; prompts, or reads stdin |
| `task cluster:plan` | show what applying would change |
| `task cluster:apply` | provision or converge the cluster |
| `task cluster:destroy` | delete the servers; asks first |
| `task cluster:kubeconfig` | write `./kubeconfig` |
| `task cluster:kubeconfig-add` | add this cluster to `~/.kube/config`, so a plain `kubectl` reaches it |
| `task cluster:talosconfig` | write `./talosconfig` |
| `task cluster:outputs` | stack outputs, secrets redacted |
| `task cluster:nodes` | list nodes |
| `task cluster:status` | nodes, then anything not Running |
| `task cluster:config-check` | Talos accepts the machine-config patches |
| `task cluster:etcd-snapshot` | snapshot etcd into `.backups/` |
| `task cluster:upgrade-talos` | upgrade Talos, one node at a time |
| `task cluster:upgrade-k8s` | upgrade Kubernetes in place |

## Layers

| Task | Does |
|------|------|
| `task platform:init ref=<org>/hetzner-cluster/<stack>` | create every layer's stack and point it at the cluster |
| `task platform:plan-all` | preview every layer in order |
| `task platform:apply-all` | apply every layer in dependency order |
| `task platform:destroy-all` | destroy every layer, in reverse |
| `task platform:plan layer=10-node-platform` | preview one layer |
| `task platform:apply layer=10-node-platform` | apply one layer |
| `task platform:destroy layer=60-observability` | destroy one layer |
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
| `task observability:check` | Alloy parses the collector config |

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
