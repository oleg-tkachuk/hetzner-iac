# Command reference

There are two entry points, and which one a command is on decides how it is
typed. `task` is the cluster: provisioning, upgrades, teardown, and everything
that reads a running one. The checks, the scanners, the formatters and the
chart pins are on the second, and every command for them says so:

    task -t Taskfile.dev.yaml verify

Task finds `Taskfile.yaml` by itself and has no environment variable for a
second file, so the flag is not optional. What it buys is the list `task`
prints on its own: cluster operations, and none of the work somebody bringing
up a cluster never runs.

`task` on its own lists the first of those. Every cluster and layer task takes
`stack=<name>`, and there is no default. A task that assumed one is a task
that can be aimed at the wrong environment by forgetting a word, so running
one without it prints the usage instead:

    $ task platform:plan layer=all
    task: platform:plan needs a stack and a layer, and neither has a default.

        task platform:plan stack=dev layer=40-ingress

There is no default because an empty one is not an error. `pulumi --stack ""`
ignores the empty value and uses the stack selected in the workspace — local,
invisible state left by the last `pulumi stack select` — so without the guard a
forgotten word aims the command at whatever that happens to be rather than
failing.

It is the only deployment parameter: where a cluster lives and how it is
shaped comes from its topology file.

The suggestion names both, because a message that named only the missing
argument handed over a command that failed on the one already passed. `layer=`
is also checked against the list of layers rather than merely for being
present, so a typo is caught before anything runs:

    $ task platform:plan stack=dev layer=30-cor
    task: ... layer has an invalid value : '30-cor'
      (allowed values : [all 10-node-platform 20-network-policy 30-cluster-services 40-ingress 50-gitops])

`task platform:init` needs no reference: it reads the cluster tier's stack name
from `infra/cluster` and writes that into every layer. `ref=` overrides it, for
a cluster in another organization or one shared by several layer stacks.

Every task that runs `pulumi up` or `pulumi destroy` asks before it does —
applying is not the safe half of the pair, because `pulumi up` replaces a
resource for any input that forces a replacement, and replacing the only
control-plane server takes the cluster down. `--yes` skips the question, which
is what a script should have to say out loud:

    $ task platform:apply layer=all stack=dev
    Apply every platform layer on dev? [y/N]

`task up` asks twice rather than three times — once for the servers, once for
everything on them — because Task prompts per task it runs and `up` adds none
of its own.

Tasks from the shared library
([oleg-tkachuk/taskfiles](https://github.com/oleg-tkachuk/taskfiles), pinned)
are trimmed with `excludes:` to what works here. Four modules are included,
split by entry point: `hcloud` and `helm` act on a live cluster and are on the
first; `go` and `security` answer questions about this repository and are on
the second. A module task that cannot succeed in this repository is worse than
a missing one: it is a command someone runs once, in an emergency, and gets a
confusing failure from.

Tasks marked **†** are the ones a workflow runs itself, so a green local run
of one is a green pull request for that check and nobody has to type it by
hand. The other checks CI performs it runs directly rather than through a task
— the unit suite, `go vet`, and golangci-lint through its own action — and
`task -t Taskfile.dev.yaml verify` and `… scan` are the local aggregates that
mirror those. `internal/ci` holds the repository's own gates, which run as
part of the unit suite. TestGateMarkers_MatchTheWorkflows keeps this marker
equal to what the workflows actually invoke, and
TestWorkflows_CallTasksThroughTheDevTaskfile keeps every workflow step on the
entry point that has the task.

## Whole platform

| Task | Does |
|------|------|
| `task build` | compile every program into `bin/` |
| `task clean` | remove build output: `bin/` and the layer binaries under `.cache` |
| `task destroy` | destroy everything: every layer, then the cluster. Asks first, and says what survives |
| `task e2e` | verify a running cluster; read-only |
| `task plan` | preview the cluster and every layer; change nothing |
| `task up` | cluster, then every layer in dependency order; asks twice |

None of the four reaches the backup tier. `up` does not create the Storage Box
and `destroy` does not take it — that is the point of it being a tier, and
[`backup:*`](#backup) below is where it is applied and destroyed.

## Cluster

| Task | Does |
|------|------|
| `task cluster:apply` | provision or converge the cluster; asks first, and cannot replace the control plane — see [what an apply cannot do](#what-an-apply-cannot-do) |
| `task cluster:audit` | who did what to the API, from every control-plane node; `last=<n>` to widen |
| `task cluster:destroy` | delete the servers; asks first. Keeps the cluster CA |
| `task cluster:encryption:check` | the system volumes are really encrypted, not just configured to be |
| `task cluster:etcd:restore` | restore etcd from `snapshot=<path>`; wipes every control-plane node first, asks first |
| `task cluster:etcd:snapshot` | snapshot etcd into `.backups/` |
| `task cluster:etcd:upload` | upload a snapshot to the Storage Box with restic; keeps the last ten |
| `task cluster:hubble` | print recent pod flows through Hubble; `last=<n>` to widen |
| `task cluster:image:bake` | bake the Talos snapshot named by the topology, per version **and** architecture; idempotent |
| `task cluster:init` | create the Pulumi stack for this environment |
| `task cluster:kubeconfig` | write `./kubeconfig` |
| `task cluster:kubeconfig:add` | add this cluster to `~/.kube/config`, so a plain `kubectl` reaches it |
| `task cluster:machine-config:check` | Talos accepts the machine-config patches |
| `task cluster:nodes` | list nodes |
| `task cluster:orphans` | Hetzner resources nothing claims, with or without a live cluster; read-only |
| `task cluster:outputs` | stack outputs, secrets redacted |
| `task cluster:recovery-kit` | everything a recovery needs that lives nowhere but Pulumi state — pipe it into a password store |
| `task cluster:plan` | show what applying would change |
| `task cluster:reboot` | reboot the nodes through Talos; they come back by themselves |
| `task cluster:secrets:destroy` | delete the cluster CA as well; unrecoverable |
| `task cluster:secrets:export` | print the Talos secrets bundle — pipe it into a password store |
| `task cluster:state:export` | every tier's Pulumi state into `.backups/state/`, secrets left encrypted |
| `task cluster:smoke` | ask whether the cluster can run a workload — nodes, a volume, a load balancer |
| `task cluster:stacks` | every stack, with what the backend and its topology say about it; takes no `stack=` |
| `task cluster:status` | nodes, then anything not Running |
| `task cluster:stop` | bring the cluster down cleanly through Talos; the instances keep existing |
| `task cluster:talosconfig` | write `./talosconfig` |
| `task cluster:token` | store the Hetzner token in the stack, encrypted; prompts, or reads stdin |
| `task cluster:upgrade:k8s` | upgrade Kubernetes in place |
| `task cluster:upgrade:talos` | upgrade Talos, one node at a time |

### What an apply cannot do

The control-plane servers and the API load balancer carry `pulumi.Protect`, so
Pulumi refuses to delete **or replace** them. The refusal happens in preview, so
nothing is touched:

```
$ task cluster:apply stack=dev          # after editing placement.location
    +-8 to replace
    5 errored
○ cluster · apply · a protected resource is the control plane or the API endpoint

    task cluster:etcd:snapshot stack=dev   # first, if etcd is running
    task cluster:apply stack=dev replace_control_plane=yes
```

The prompt cannot cover this case, because the plan does not look like the thing
it is. Editing `placement.location` reads like a move; what it plans is a
replacement of all **three** control-plane nodes. They have no dependency on
each other — measured on the live stack, each depends on the network and the
placement group and nothing else — and `pulumi up --parallel` defaults to 56, so
they go at once. `DeleteBeforeReplace` is set deliberately, because a Hetzner
server name is unique in the project, which means each node is deleted before
its replacement exists. etcd does not survive that; the way back is a snapshot.

The API load balancer is protected for a different reason: its address **is**
the cluster endpoint. Every certificate names it and both configs point at it,
so a replacement hands back an address nothing is configured for.

`replace_control_plane=yes` passes `--ignore-protect` for that one run. Worker
pools are not protected — they are replaceable by design, and a refusal that
fires on ordinary work is one that gets bypassed by habit.

`task cluster:destroy` uses the same flag, because a teardown IS meant to take
them, and keeps the cluster CA with one `--exclude`. It checks that the exclusion
matches exactly one resource first: an `--exclude` that matches nothing is
silent, and measured on this stack a bogus one previewed "22 to delete" — the
whole cluster, CA included.

### Policy

| Task | Does |
|------|------|
| `task policy:check` | run the CrossGuard pack over every tier and every layer; changes nothing |
| `task policy:tier` | run it over one tier — `tier=cluster`, `tier=backup` |
| `task policy:layer` | run it over one layer — `layer=40-ingress` |

Why a policy pack when the components validate: see
[configuration.md](configuration.md#what-the-policy-pack-enforces).

`policy:check` walks every tier and every layer; `policy:tier` and
`policy:layer` narrow to one of either. There used to be a task that could only
mean the cluster tier, which left the backup tier with no narrowing at all.

## Backup

A tier of its own rather than a platform layer, so it is neither in
`layer=all`'s order nor in what `task destroy` takes — the reasons are in
[design.md](design.md#what-decides-a-layer-boundary). It creates the Hetzner
Storage Box that `task cluster:etcd:upload` sends snapshots to, and generates
the three credentials that reach it, into its own state.

| Task | Does |
|------|------|
| `task backup:init` | create the tier's stack and point it at the cluster; `ref=` overrides the derived reference |
| `task backup:plan` | preview it; changes nothing |
| `task backup:apply` | create the Storage Box and its credentials; asks first, because the box is billable |
| `task backup:destroy ignore_protect=yes` | destroy it; refuses without that argument, then asks |
| `task backup:outputs` | the sftp destination, secrets redacted |

`backup:destroy` is the only command that can take the Storage Box, and it
takes three things to say so: the argument, the prompt, and the fact that the
task is not in any walk.

The box is `pulumi.Protect(true)`, so Pulumi itself refuses to delete **or
replace** it. Measured on a scratch stack: the destroy fails during PREVIEW, so
nothing in the stack is deleted — not even the unprotected resources beside it.
That is the state Hetzner's own delete protection cannot produce; it guards the
console, the API and the hcloud CLI, and the provider clears it before its own
delete, measured as 17 seconds to remove a box with a snapshot on it.

`ignore_protect=yes` passes `--ignore-protect`, scoped to that one operation.
`pulumi state unprotect` would do it too and is deliberately not used:
it edits the state and leaves it edited, so a teardown that fails half way
leaves the destination unprotected with nothing saying so.
`cluster:secrets:destroy` made the same choice for the cluster CA.

The price of the protection is named in the code: a change that forces
replacement — the box type, its location — fails the same way, and that one is
fixed only by removing the option and applying. Resizing the backup destination
is a code change, which is the right cost for a resource whose deletion takes
every snapshot with it.

The restic repository password is generated into this tier's state and nothing
types it, so re-applying the tier after destroying it produces a password that
does not open the existing repository — see
[recovery.md](recovery.md#the-three-parts-of-a-backup).

## Hetzner instances

From the shared library's `hcloud` module, not this repository. `console` takes
`HCLOUD_SERVER=<name>`; the rest act on every server of the cluster.

| Task | Does |
|------|------|
| `task hcloud:console` | VNC console on one node, `HCLOUD_SERVER=<name>`; the only way to watch a node that will not boot |
| `task hcloud:poweroff` | cut power, for when Talos cannot answer |
| `task hcloud:poweron` | power the instances on |
| `task hcloud:reboot` | ACPI reboot — for a kernel that lives when apid does not |
| `task hcloud:reset` | hard reset: a power cut and a start in one |
| `task hcloud:servers` | power state of every server in this cluster; read-only |
| `task hcloud:shutdown` | ACPI shutdown — the power button, which Talos acts on |

## Layers

| Task | Does |
|------|------|
| `task helm:list` | every Helm release on the cluster |
| `task platform:apply layer=10-node-platform` | apply one layer, or `layer=all` in dependency order; asks first |
| `task platform:apply layer=30-cluster-services target=cert-manager` | apply one component of one layer — see [narrowing to one component](#narrowing-to-one-component) |
| `task platform:destroy layer=50-gitops` | destroy one layer, or `layer=all` in reverse; asks first |
| `task platform:destroy layer=30-cluster-services target=cert-manager` | destroy one component; shows what it takes first, and refuses to take dependents without `dependents=yes` |
| `task platform:init` | create every layer's stack and point it at the cluster |
| `task platform:layers` | the layer order, in dependency order |
| `task platform:outputs layer=50-gitops` | one layer's stack outputs, or `layer=all` |
| `task platform:plan layer=10-node-platform` | preview one layer, or `layer=all` for every one in order |
| `task platform:plan layer=30-cluster-services target=group:Ingress` | preview one component or group |
| `task platform:refresh layer=40-ingress` | reconcile one layer's state with the cloud, or `layer=all`; asks first, and writes state |
| `task platform:status` | which layers are deployed, and how large |

### Narrowing to one component

`plan`, `apply` and `destroy` take an optional `target=`, which becomes
`pulumi --target`:

| Selector | Selects |
|----------|---------|
| `target=cert-manager` | the resource with that name |
| `target=ConfigFile:kubelet-serving-cert-approver` | the same, qualified, when one name is used by two types |
| `target=group:Ingress` | a group's own node and everything under it |
| `target=hcloud-ccm,hcloud-csi` | both, as one run with two `--target` flags |

A comma-separated list is one `pulumi` run rather than one per component, which
is worth more than the typing: two runs are two chances for the second to act
on a cluster the first one changed. Every selector in the list has to resolve
or the whole list is refused — resolving the good half would be exactly the
apply-that-did-less-than-asked this validation exists to prevent.

`layer=all` is refused with a target, because a URN names one stack.

**The name is checked before Pulumi runs**, and that is the reason
[`tools/target`](../tools/target) exists rather than the taskfile passing
`--target` straight through. A `--target` that matches nothing SUCCEEDS —
measured on this repository's dev stack, `preview --target
'**::Release::does-not-exist'` reported `24 unchanged` and exited zero. So a
mistyped component would be an apply that claims to have worked. A refusal
lists what the stack holds instead.

**A targeted apply leaves the rest of the layer on its last full apply's
inputs.** Pulumi's own documentation says so — "the targeted resource may end
up with stale input values" — and neither the state nor the output looks
different afterwards. So the task says it, with `▲`, which is the one glyph that
survives a Pulumi run:

```
▲ platform · 30-cluster-services · targeted: everything else kept its last-applied inputs
```

Two consequences worth holding on to: a plan taken from a targeted preview
cannot be applied by a full `up`, and the next full apply may show a diff nobody
wrote.

**A targeted destroy is the strict one.** `pulumi destroy --target` FAILS when
the target has dependents and `--target-dependents` is not given, and takes them
when it is. The task therefore shows the plan first, with `--preview-only`, and
keeps the flag behind a separate `dependents=yes` — removing more than was asked
for should be something an operator typed.

`--exclude` is deliberately absent. It fails in the safe direction: a mistyped
exclusion applies MORE than intended, which is a full apply, while a mistyped
target applies nothing. It needs none of the validation above, and belongs in
its own change if the need arises.

## Working on this repository

Everything below is on the second entry point, so every command carries
`-t Taskfile.dev.yaml`. They live in
[`Taskfile.dev.yaml`](../Taskfile.dev.yaml) rather than beside the cluster
tasks for two reasons: `task` on its own should list operations rather than
checks, and two of these read a pinned version out of
`.github/workflows/ci.yaml` — a clone that only wants to apply a cluster
should not need a pipeline's configuration to do it.
TestRootTaskfile_HoldsNoCheck keeps them here.

### Checks

| Task | Does |
|------|------|
| `task -t Taskfile.dev.yaml verify` | everything checkable without a cluster — needs helm, kubeconform, talosctl and lychee |
| `task -t Taskfile.dev.yaml scan` | every scanner CI runs — gitleaks, trivy, govulncheck, gosec, checkov |
| `task -t Taskfile.dev.yaml lint` | golangci-lint at the version CI pins, and refuses another; `-- ./internal/...` narrows it |
| `task -t Taskfile.dev.yaml lint:install` | write that pinned version into `bin/`, for this platform |
| `task -t Taskfile.dev.yaml shell` † | shellcheck over the shell inside the taskfiles — needs shellcheck |
| `task -t Taskfile.dev.yaml fmt` | format and tidy |
| `task -t Taskfile.dev.yaml fmt-check` † | fail if `gofmt -s` would change anything |
| `task -t Taskfile.dev.yaml docs:links` † | do the documentation's own links point at files and headings that exist? — needs lychee |
| `task -t Taskfile.dev.yaml checkov:scan` † | hardening rules over the manifests and workflows this repository ships |
| `task -t Taskfile.dev.yaml checkov:triage` | every finding, including the ones `.checkov.yaml` skips — for deciding what to fix, never a gate |
| `task -t Taskfile.dev.yaml layers` † | the layer order, for CI's matrix — a pass-through to `platform:layers` |

### Charts

| Task | Does |
|------|------|
| `task -t Taskfile.dev.yaml charts:appversions` | each `AppVersion` is what the pinned chart ships |
| `task -t Taskfile.dev.yaml charts:list` | every pinned chart |
| `task -t Taskfile.dev.yaml charts:outdated` | each pin against the latest upstream chart |
| `task -t Taskfile.dev.yaml charts:render-check` | the charts still produce the workloads and honour the values |
| `task -t Taskfile.dev.yaml charts:validate` | pins are exact versions, not floating tags |

### Code

| Task | Does |
|------|------|
| `task -t Taskfile.dev.yaml go:compile` | type-check without writing a binary |
| `task -t Taskfile.dev.yaml go:deps:outdated` | dependencies with a newer version available |
| `task -t Taskfile.dev.yaml go:deps:update` | bump the direct dependencies, tidy, then prove it still builds |
| `task -t Taskfile.dev.yaml go:fmt` | format |
| `task -t Taskfile.dev.yaml go:fmt:check` | fail if `gofmt -s` would change anything; what `fmt-check` runs |
| `task -t Taskfile.dev.yaml go:lint` | golangci-lint from PATH, whichever version that is |
| `task -t Taskfile.dev.yaml go:test` | the unit suite |
| `task -t Taskfile.dev.yaml go:test:coverage` | unit suite with an HTML coverage report |
| `task -t Taskfile.dev.yaml go:test:tagged:compile` | type-check the `e2e` suite, which the default run never compiles |
| `task -t Taskfile.dev.yaml go:tidy` | tidy the module |
| `task -t Taskfile.dev.yaml go:vuln` | govulncheck |

What each suite proves, and why the e2e one does not run in CI:
[ci.md](ci.md#what-the-suites-prove). `task e2e` is on the other entry point —
it needs a cluster.

### Security

| Task | Does |
|------|------|
| `task -t Taskfile.dev.yaml security:gosec` † | insecure patterns the compiler is happy with |
| `task -t Taskfile.dev.yaml security:secrets` † | gitleaks over the whole history |
| `task -t Taskfile.dev.yaml security:trivy` † | vulnerable dependencies and secrets, plus IaC misconfig |
| `task -t Taskfile.dev.yaml security:vuln` † | govulncheck across every module |
| `task -t Taskfile.dev.yaml security:lint` | golangci-lint across every module; CI runs the linter through its own action, not this |
| `task -t Taskfile.dev.yaml security:scan` | the module's own aggregate: secrets, filesystem, Go vuln, lint and SAST. `scan` above runs the four CI runs instead, not this |

## Running it day to day

Stopping and starting, reaching the cluster with a plain `kubectl`, the checks
worth running and where the wrappers stop: [operations.md](operations.md),
and [recovery.md](recovery.md) for upgrades, snapshots and restores.
