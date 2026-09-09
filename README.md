# hetzner-iac

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
| [Task](https://taskfile.dev/installation/) 3.53+ | the entry points |
| [hcloud CLI](https://github.com/hetznercloud/cli) | inspection, and baking the Talos image |
| [hcloud-upload-image](https://github.com/hetznercloud/hcloud-upload-image) | Hetzner has no custom-image upload API |
| [talosctl](https://www.talos.dev/latest/introduction/getting-started/) | day-2: upgrades, etcd snapshots |
| `jq`, `curl` | used by the image-bake task |

A Hetzner Cloud API token with read+write scope on the project.

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

## Everyday tasks

```bash
task                                  # list everything
task plan stack=prod                  # preview the cluster and every layer
task platform:apply layer=20-cni      # one layer
task platform:status stack=prod       # what is deployed
task verify                           # what CI checks: fmt, tests, lint, vuln, pins
task charts:outdated                  # chart pins vs upstream
```

Day 2 lives under `cluster:` — `etcd-snapshot`, `upgrade-talos`, `upgrade-k8s`.
Upgrading Talos means bumping `talos.version` in the topology, re-running
`cluster:image-bake`, then `cluster:upgrade-talos`. Nodes are upgraded in
place; they are never replaced, which is why the server resource ignores
changes to its image.

## Configuration

Cluster shape lives in `infra/cluster/cluster.<stack>.yaml`. Everything else is
Pulumi config:

| Key | Where | Meaning |
|-----|-------|---------|
| `hcloud:token` | `infra/cluster` | Hetzner API token (secret) |
| `hetzner-cluster:publicIPv4` | `infra/cluster` | routable address per node; required unless you apply from inside the private network |
| `<layer>:clusterStackRef` | every layer | `<org>/hetzner-cluster/<stack>` |
| `cloud-integration:hcloudToken` | `10-cloud-integration` | token for the CCM and CSI (secret) |
| `core:acmeEmail` | `30-core` | enables the Let's Encrypt ClusterIssuer; omit it and none is created |
| `gitops:domain` | `50-gitops` | publishes Argo CD through ingress; omit it and there is no Ingress |
| `observability:metricsRetention` | `60-observability` | default `30d` |

The Hetzner token is read by the cloud-integration layer rather than exported
by the cluster tier: a stack that exports a cloud credential puts it into the
state of every stack that references it.

## Testing

```bash
go test ./...        # unit: topology validation, addressing, machine config,
                     # chart pins, and the resource graph under Pulumi's mocks
task e2e stack=prod  # against a real cluster; read-only, safe in production
```

The unit tests pin what Pulumi will *ask for*, including the settings whose
mismatch never fails an apply — kube-proxy replacement, PROXY protocol on both
sides of the load balancer, the KubePrism port. The e2e suite checks what
actually happened.

## Layout

```
infra/cluster/     the Hetzner cluster: network, firewall, control plane, workers
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
