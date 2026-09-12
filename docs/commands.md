# Command reference

`task` on its own lists everything. Every cluster and layer task takes
`stack=<name>`, defaulting to `dev` — that is the only deployment parameter,
because where a cluster lives and how it is shaped comes from its committed
topology file.

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
