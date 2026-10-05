# Running it day to day

A cluster that already exists: stopping it, starting it, checking it, and
reaching it with a plain kubectl. Upgrades, snapshots and restores are
[recovery.md](recovery.md); building a cluster is
[the command reference](commands.md); why it is shaped this way is
[design.md](design.md).

## Stopping and starting

| | Soft | Hard | Back on |
|---|---|---|---|
| the **instance** | `hcloud:shutdown` | `hcloud:poweroff` | `hcloud:poweron` |
| the **cluster** | `cluster:stop` | — | `hcloud:poweron` |

| Restart | Needs | Use when |
|------|-------|----------|
| `cluster:reboot` | Talos answering | normally — etcd closes its log |
| `hcloud:reboot` | the kernel running | apid has stopped answering |
| `hcloud:reset` | nothing | the node is gone; etcd recovers its log on boot |

Prefer `cluster:stop`: Talos shuts etcd down cleanly. The `hcloud:` variants say
nothing to Kubernetes — `poweroff` cuts power mid-write. A stopped machine runs
no apid, so only `hcloud:poweron` brings it back.

**Stopping does not save money.** Hetzner bills a server until it is deleted
([billing](https://docs.hetzner.com/cloud/billing/)); to stop paying,
`task cluster:destroy`. Encrypted volumes unlock on their own after a power
cycle — the key derives from the node's UUID
([design.md](design.md#what-the-cluster-encrypts-and-what-it-does-not)).

### The `hcloud:` tasks

They come from the shared library's
[`hcloud` module](https://github.com/oleg-tkachuk/taskfiles/blob/main/hcloud/README.md)
and act on every server carrying the cluster's `cluster=<name>` label, which is
what makes them safe on a shared project. `hcloud:console` takes
`HCLOUD_SERVER=<name>` and refuses a server outside the selector; what it prints
is a short-lived root console credential, so keep it out of logs.

Anything else — rebuild, rescue, resize, snapshots — fights Pulumi for the
server or means nothing to Talos, so it is not wrapped. Use the CLI:

```bash
export HCLOUD_TOKEN="$(go run ./tools/token dev)"
hcloud server describe platform-dev-control-plane-0
```

## Who did what

The API server writes an audit log on every control-plane node, at
`/var/log/audit/kube/kube-apiserver.log`, rotated at 100 MB and kept 30 days or
10 files. The policy is
[`internal/pkg/clusterspec/auditpolicy.yaml`](../internal/pkg/clusterspec/auditpolicy.yaml).

```bash
task cluster:audit stack=dev            # last 200 events per node, merged
task cluster:audit stack=dev last=5000
```

- **Most events are `Metadata`** — who, when, verb, resource, code, no body.
  RBAC changes, `pods/exec`, `attach`, `portforward` and changes to admission
  webhooks and admission policies record their request body. Health checks, discovery, kubelet reads
  and leases are dropped.
- **Secrets are never recorded with a body.** `secrets`, `configmaps` and
  `serviceaccounts/token` are pinned at `Metadata` by the first rule, so no
  later rule can raise or drop them.
- **The log lives on EPHEMERAL.** `talosctl reset`, a replaced node and
  `cluster:etcd:restore` all take it, so recovering from an incident destroys
  its record. Shipping it off the node is not built.

## Changing one component

`plan`, `apply` and `destroy` take `target=` — see
[commands.md](commands.md#narrowing-to-one-component).

Use it when only one chart's values changed. Everything it does not touch
keeps its last full apply's inputs, so the task marks the run:

```
▲ platform · 30-cluster-services · targeted: everything else kept its last-applied inputs
```

After targeted applies, run a full one so the layer matches its program again.
A targeted **destroy** shows its plan first; read it — `dependents=yes` takes
every resource that depends on the target too.

## Checks worth running

| Task | Answers |
|------|---------|
| `task cluster:smoke` | can the cluster run a workload — see below |
| `task cluster:status` | are the nodes Ready, and is anything not Running |
| `task platform:status` | every stack's last run, interrupted updates and the `refresh` that clears them |
| `task platform:drift` | which resources the cloud no longer agrees with |
| `task cluster:encryption:check` | are the system volumes encrypted on disk, not only in config |
| `task cluster:orphans` | is anything left that no stack holds and the cluster does not use |
| `task cluster:hubble` | the cluster's traffic, as flows |
| `task cluster:machine-config:check` | does Talos accept the machine-config patches |

`encryption:check` exists because Talos encrypts a volume only when its
partition is empty: config applied to an existing node is accepted and changes
nothing. `orphans` finds volumes a destroyed cluster could no longer delete; it
also works when the cluster is gone.

## Does the cluster actually work?

`pulumi up` succeeding does not mean a workload can run — it once went green
with the CSI controller crash-looping.

```bash
task cluster:smoke stack=dev              # verbose=yes also prints each probe
```

| Check | Proves |
|---|---|
| every node is `Ready` | the CNI is installed |
| a pod reaches a pod on another node | the CNI routes between nodes |
| a claim on `hcloud-volumes` reaches `Bound` | the CSI driver, through the Hetzner API |
| volumes holding data are on a class that retains them | a namespace labelled `hetzner-iac/holds-data` loses nothing when a claim is deleted |
| every secret store is ready | the External Secrets Operator reaches Pulumi ESC |
| every network policy is valid | Cilium accepted every policy the API server stored |
| every LoadBalancer Service has an address and somewhere to send it | the cloud controller manager |
| the external metrics API answers | KEDA, when `kedaEnabled` is set |
| an image outside the inventory is refused | the image policies are enforced (see [below](#which-images-may-run)) |

The cross-node check asks cluster DNS from a node with no DNS replica, so the
answer must cross a node boundary. The storage check schedules a pod because
`hcloud-volumes` binds on first consumer, and deletes both probes even when it
fails.

```
  ✔ every node is Ready
      3 Ready
  ✖ a claim on hcloud-volumes reaches Bound
      claim is Pending after 2m0s. Last event: …
  ○ every LoadBalancer Service has an address and somewhere to send it
      no Service of type LoadBalancer exists, so this proves nothing about the cloud controller manager

  1 passed · 1 skipped · 1 failed
```

**Skipped is not passed:** `○` means there was nothing to check. Exit code `2`
means the checks ran and failed, `1` that they could not run; `go run` and
`task` flatten both to `1`, so build the binary (`go build -o smoke
./tools/smoke`) if the difference matters.

## Which images may run

[`internal/pkg/imagepolicy/images.yaml`](../internal/pkg/imagepolicy/images.yaml)
lists every image repository the charts run, with who signs it or, for an
unsigned one, the tag and digest it is pinned to. `30-cluster-services`
generates a ClusterImagePolicy per entry for the Sigstore policy-controller.

- **Enforced** in every namespace a chart installs into, except `kube-system`
  and `cosign-system` — a webhook outage there would stop the CNI or the webhook
  itself. An image with no entry is refused.
- **Argo CD, Dex, KEDA and Cilium stay at warn.** They sign only as Sigstore
  bundles, which policy-controller cannot verify yet.
- **A chart bump that moves an unsigned image's tag** fails `charts render`
  until the pin follows. Renovate runs `go run ./tools/charts repin` on every
  chart bump; run it by hand otherwise.
- **A new image in a chart** needs an entry before `charts render` passes:
  verify its signature with `cosign verify`, or pin it.

## Reaching the cluster with a plain kubectl

Tasks and Pulumi pass `--kubeconfig` explicitly; a bare `kubectl` does not.
Either merge at read time, touching no file:

```bash
export KUBECONFIG=$HOME/.kube/config:$PWD/kubeconfig
```

or add the cluster to `~/.kube/config` once:

```bash
task cluster:kubeconfig:add stack=dev
```

It backs the file up, refuses if a cluster of that name already points
elsewhere, keeps the client key off disk, and leaves the current context alone.

### From a machine the firewall does not know

Only two ports are open — the Kubernetes API and the Talos API — and only from
`network.adminCIDRs`. From any other address both time out, which looks like a
broken cluster and is a filtered port.

Widening the list needs the topology file, `infra/cluster/cluster.<stack>.yaml`,
which is gitignored and is the one input the stack does not give back. Keep a
copy with the Hetzner token. If it is lost, rebuild it from
`cluster.example.yaml` and `pulumi stack export`, then check with
`task cluster:plan`: the only change should be the firewall.

`task hcloud:*` works regardless — it goes through the Hetzner API.

## Control-plane addresses

Each control-plane node holds a Hetzner Primary IP with auto-delete off, so a
replaced node keeps the address the kubeconfig and talosconfig name. Workers
keep implicit addresses.

A server's public network is never updated after creation: the hcloud provider
does that by powering the server off and, in some states, deleting its address.

### Moving a cluster that predates this

A cluster built before v7 has implicit addresses; a plain apply would create
three new unassigned ones. Adopt the existing ones instead — no server is
touched.

1. Find each control-plane node's address id and match `assignee_id` to the
   server ids in `pulumi stack export`:

   ```bash
   bash -c 'cd infra/cluster && HCLOUD_TOKEN="$(pulumi config get hcloud:token --stack <stack>)" hcloud primary-ip list -o json | jq -r ".[] | \"\(.id) \(.ip) server \(.assignee_id)\""'
   ```

2. Import them as `<node>-ipv4` under the control-plane component:

   ```json
   {
     "nameTable": {"cp": "urn:pulumi:<stack>::hetzner-cluster::hetzner-iac:cluster:Cluster$hetzner-iac:cluster:ControlPlane::<cluster>-control-plane"},
     "resources": [
       {"type": "hcloud:index/primaryIp:PrimaryIp", "name": "<cluster>-control-plane-0-ipv4", "id": "<id>", "parent": "cp"}
     ]
   }
   ```

   ```bash
   bash -c 'cd infra/cluster && pulumi import --stack <stack> --file <that file> --generate-code=false --protect'
   ```

3. Drop the imported `assigneeId` input, which the provider refuses beside a
   `location`:

   ```bash
   bash -c 'cd infra/cluster && pulumi stack export --stack <stack> > state.json && jq "(.deployment.resources[] | select(.type==\"hcloud:index/primaryIp:PrimaryIp\") | .inputs) |= del(.assigneeId)" state.json > state.new.json && pulumi stack import --stack <stack> --file state.new.json'
   ```

4. `task cluster:plan stack=<stack>` should show the three addresses renamed,
   labelled and set to auto-delete off, and nothing else. Apply it.
