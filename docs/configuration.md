# Configuration

Three places hold the configuration of a DEPLOYMENT:

| Where | Holds |
|-------|-------|
| `infra/cluster/cluster.<stack>.yaml` | everything that shapes a cluster; not committed |
| Pulumi stack config | the Hetzner token, and what each layer deploys |
| `internal/pkg/charts` | every chart version |

## Where configuration lives

Read this to find the file; read the sections below for what its fields mean.

**A deployment — what an operator edits**

| File | Configures |
|------|------------|
| `infra/cluster/cluster.<stack>.yaml` | the cluster: size, locations, versions, addressing, encryption. Not committed — start from [cluster.example.yaml](../infra/cluster/cluster.example.yaml) |
| [cluster.schema.json](../infra/cluster/cluster.schema.json) | what a topology may say, checked in the editor as it is typed |
| `Pulumi.<stack>.yaml` in each project | that stack's own config: the Hetzner token, the reference to the cluster, per-layer switches. Not committed |
| `Pulumi.yaml` in each project | the project itself — its name, runtime and the config keys it accepts ([cluster](../infra/cluster/Pulumi.yaml), [backup](../infra/backup/Pulumi.yaml), and one per layer) |

**The platform — what the repository decides once for every cluster**

| File | Configures |
|------|------------|
| [internal/pkg/charts](../internal/pkg/charts) `<chart>.go` | one file per chart: its version and repository, the layer that installs it, the objects it must produce, and the value keys whose misspelling fails silently |
| [internal/pkg/charts](../internal/pkg/charts) `*.yaml.tmpl` | the Helm values, one template per chart beside its declaration — what actually reaches Helm |
| [internal/pkg/clusterspec](../internal/pkg/clusterspec) | the topology's schema in Go, its defaults, and the Talos machine-config patches |
| [internal/pkg/clusterspec/auditpolicy.yaml](../internal/pkg/clusterspec/auditpolicy.yaml) | what the API server audit log records |
| [internal/pkg/platform](../internal/pkg/platform) | names two layers must agree on: storage classes, the ingress class, the issuer, node ports |
| [internal/pkg/clusterref](../internal/pkg/clusterref) | the cluster tier's stack outputs, by name — the contract every layer reads |
| [layers/20-network-policy/manifests](../layers/20-network-policy/manifests) | the cluster-wide network policy, one file per rule |
| [layers/30-cluster-services/manifests](../layers/30-cluster-services/manifests) | the manifests that layer applies alongside its charts |

**The repository — how it runs and what it refuses**

| File | Configures |
|------|------------|
| [Taskfile.yaml](../Taskfile.yaml) | the cluster commands, the layer order `layer=all` walks, and the pinned versions of the shared task library and [pulumi-kit](https://github.com/oleg-tkachuk/pulumi-kit) |
| [Taskfile.dev.yaml](../Taskfile.dev.yaml) | the checks, scanners and formatters — the second entry point |
| [tasks/](../tasks) | one taskfile per area: cluster, platform, backup, charts, policy |
| [policy/](../policy) | the CrossGuard pack every project is previewed against |
| [.github/workflows/](../.github/workflows) | what CI runs: checks, security, nightly, release, Renovate |
| [.golangci.yaml](../.golangci.yaml) | the Go linters, at the version CI pins |
| [.checkov.yaml](../.checkov.yaml) | which hardening rules apply to the manifests and workflows |
| [.trivyignore.yaml](../.trivyignore.yaml) | the vulnerability findings accepted, each with a reason |
| [.github/zizmor.yml](../.github/zizmor.yml) | the workflow-security scanner's own rules |
| [.github/renovate.json](../.github/renovate.json) | how dependency and chart updates are proposed |
| [lefthook.yml](../lefthook.yml) | the git hooks: what cannot be committed |
| [Brewfile](../Brewfile) | the tools a machine needs to run any of this |
| [.gitignore](../.gitignore) | what must never be committed — credentials, topologies, state exports |

**Generated, never committed**: `kubeconfig` and `talosconfig` (written by
`task cluster:kubeconfig` and `cluster:talosconfig`), and the values files
rendered from the templates above. Both configs are cluster-admin.

## The topology file

Everything that shapes a cluster lives in
`infra/cluster/cluster.<stack>.yaml`, validated by `cluster.schema.json` as you
type it and by the same Go code the Pulumi program runs when you apply.

It is sparse: anything omitted keeps the default in `internal/pkg/clusterspec`.
Start from [cluster.example.yaml](../infra/cluster/cluster.example.yaml), which
carries an RFC 5737 placeholder in `network.adminCIDRs` — replace it with the
address you will apply from, or Talos configuration cannot be pushed and the
apply hangs with the port filtered.

### What goes in `network.adminCIDRs`

The public egress address of every machine that administers the cluster: the
one running `task cluster:*` and `task platform:*`, and anywhere `kubectl` or
`talosctl` is used. The firewall opens the Kubernetes API (tcp/6443) and the
Talos API (tcp/50000) to these ranges and to nothing else, and the API load
balancer has no public interface, so this list is the whole of the perimeter.

```bash
curl -s https://api.ipify.org
```

prints the address a machine is seen from. Use it as a `/32` when it is fixed.
When it moves, choose between a `/32` added each time it changes and the
provider's range, which admits everyone else on it —
[operations.md](operations.md#from-a-machine-the-firewall-does-not-know) has
that trade-off and the way back in when the list is wrong.

Never commit it. `infra/cluster/cluster.*.yaml` is ignored except the example,
and `TestTrackedFiles_NameNoRealAddressOfTheirOwn` fails CI on a real address
in any tracked file. A change reaches Hetzner through `task cluster:apply`,
which updates the firewall in place.

## How pod traffic crosses nodes

`network.routingMode` is `native` or `tunnel`, and the default is `native`.

| | native | tunnel |
|---|---|---|
| pod packet to another node | to the private gateway, routed by the CCM's per-node routes | wrapped in VXLAN, node address to node address |
| needs the private network to route pod CIDRs | yes | no |
| needs the CCM's route controller | yes | no |
| overhead | none | ~50 bytes a packet, and the MTU |
| packet captures | readable | encapsulated |

Pick `tunnel` when the private network's routing is what you are debugging, or
on a provider whose network does not route pod CIDRs. Otherwise `native`.

Switching is a **maintenance operation**: every Cilium agent restarts and pod
traffic breaks while they do. It is one value and no Talos apply — the gateway
route is in the machine config in both modes.
[networking.md](networking.md#how-pod-traffic-crosses-a-node-boundary) has the
mechanism.

## CPU architecture

`talos.architecture` is `x86` (the default) or `arm`, and one field decides
three things:

| | `x86` | `arm` |
|---|---|---|
| image the factory builds | `hcloud-amd64.raw.xz` | `hcloud-arm64.raw.xz` |
| server types that can boot it | `cx`, `cpx`, `ccx` | `cax` only |
| default control plane / worker | `cx23` / `cx33` | `cax11` / `cax21` |

Leave the server types out and they follow the architecture. Name one from the
wrong side and it is refused at plan time, before anything is created:

```
cx23 is x86, but the baked Talos image is arm — a mismatched type will not boot
```

Switching the field means re-baking: `task cluster:image:bake`. A snapshot is
one architecture, and the bake looks only for a snapshot of the architecture
the topology asks for.

**Arm is not the cheaper option on Hetzner**: a `cax` type costs more than the
`cx` type with the same cores and memory. Every image the platform installs
publishes `linux/arm64` at the versions pinned in
[`internal/pkg/charts`](../internal/pkg/charts), so the reason to pick `arm` is
wanting Arm nodes, not saving money.

### Which locations have Arm, and why that is not the blocker here

Hetzner's [locations table](https://docs.hetzner.com/cloud/general/locations/)
lists **Cloud Shared `AMPERE`** — the `cax` line — in three locations only:

| | `fsn1` | `nbg1` | `hel1` | `ash` | `hil` | `sin` |
|---|---|---|---|---|---|---|
| Cloud Shared `AMPERE` | ✓ | ✓ | ✓ | ✗ | ✗ | ✗ |

Even there the API can refuse every `cax` create with
`unsupported location for server type (invalid_input)` while an `x86` create in
the same project and location succeeds. The refusal follows the architecture,
not the location or capacity, and the API's own availability data does not
predict it: **it needs a support ticket, not a wait.**

Before planning an Arm cluster, probe one server by hand — with an Arm image by
id, not by name, because Hetzner answers an x86 image on an Arm type with the
*same* message:

```bash
hcloud image list --type system --architecture arm -o columns=id,name
hcloud server create --name arch-probe --type cax11 --image <id> --location hel1
```

Delete the probe if it succeeds — a server costs from the moment it exists.

## What the policy pack enforces

Validation inside a component binds only callers that go through it:
`internal/pkg/clusterspec.BuildFirewallRules` refuses an empty
`network.adminCIDRs`, then appends `FirewallRuleOptions.Extra` without looking
at the sources, so an extra rule opening `6443` to `0.0.0.0/0` passes it.
[`policy/`](../policy) closes that with CrossGuard, which checks the resources
a program declares:

| Policy | Refuses |
|---|---|
| `hcloud-admin-ports-not-world-open` | `6443` or `50000` reachable from `0.0.0.0/0` or `::/0` |
| `hcloud-server-joins-private-network` | a server with no network attachment — etcd and kubelet ride `network.nodeSubnet` |
| `hcloud-server-deletes-before-replace` | a server replaced create-first, which Hetzner refuses on the unique name with the old server still standing |
| `helm-release-pins-chart-version` | a release that resolves to whatever the repository serves today |
| `cluster-tier-keeps-its-protections` | in the stack that holds the servers: anything but one protected Talos secrets bundle, or an unprotected control-plane server or API load balancer |

```bash
task policy:check stack=dev
```

It previews the cluster tier and every layer and changes nothing.

### It is a gate, not yet a control

`pulumi preview --policy-pack` is **local** enforcement: omitting the flag
skips it, so it catches mistakes rather than preventing them. The mode that
cannot be skipped is a Pulumi Cloud organisation policy group, which applies to
every stack in the organisation. That is an account setting, not a repository
change, and stays with whoever owns the organisation.

## Stack config

| Key | Where | Meaning |
|-----|-------|---------|
| `hcloud:token` | [`infra/cluster`](../infra/cluster) | Hetzner API token (secret); `cluster:image:bake` decrypts it from here too |
| `<layer>:clusterStackRef` | every [layer](../layers) | `<org>/hetzner-cluster/<stack>`; written by `platform:init` |
| `node-platform:hcloudToken` | [`10-node-platform`](../layers/10-node-platform) | optional; overrides the token the cluster stack exports (secret) |
| `node-platform:cni` | [`10-node-platform`](../layers/10-node-platform) | which CNI to install; empty means `cilium`, the only one implemented |
| `network-policy:enabled` | [`20-network-policy`](../layers/20-network-policy) | create the policies; `false` by default, see [networking.md](networking.md#the-default-deny-is-opt-in) |
| `cluster-services:acmeEmail` | [`30-cluster-services`](../layers/30-cluster-services) | enables the Let's Encrypt ClusterIssuer; omit it and none is created |
| `cluster-services:acmeStaging` | [`30-cluster-services`](../layers/30-cluster-services) | order from Let's Encrypt's staging endpoint: untrusted certificates, and where a new domain's first attempt belongs |
| `cluster-services:kedaEnabled` | [`30-cluster-services`](../layers/30-cluster-services) | install KEDA, the event-driven autoscaler; it scales pods and not nodes, so it cannot grow past the pinned worker pools |
| `cluster-services:pulumiAccessToken` | [`30-cluster-services`](../layers/30-cluster-services) | token the External Secrets Operator reads Pulumi ESC with; empty creates no store, and it is set with `--secret` (see [secrets from Pulumi ESC](#secrets-from-pulumi-esc)) |
| `ingress:hcloudToken` | [`40-ingress`](../layers/40-ingress) | optional; overrides the token the cluster stack exports for the load balancer and DNS records (secret) |
| `ingress:loadBalancerType` | [`40-ingress`](../layers/40-ingress) | Hetzner load balancer type, default `lb11` |
| `gitops:repoURL` | [`50-gitops`](../layers/50-gitops) | the repository Argo CD reconciles; omit it and Argo CD is installed and reconciles nothing |
| `gitops:path` | [`50-gitops`](../layers/50-gitops) | where the tree of Applications starts in that repository, default the root |
| `gitops:revision` | [`50-gitops`](../layers/50-gitops) | branch, tag or commit to track, default `HEAD` |
| `gitops:repoUsername` | [`50-gitops`](../layers/50-gitops) | HTTPS username for a private repository; any non-empty string when the password is a token (secret) |
| `gitops:repoPassword` | [`50-gitops`](../layers/50-gitops) | HTTPS password or token for a private repository (secret) |
| `gitops:repoSSHPrivateKey` | [`50-gitops`](../layers/50-gitops) | SSH private key for a private repository, in place of the two above — a read-only deploy key is enough (secret) |
| `backup:storageBoxType` | [`backup`](../infra/backup) | Hetzner Storage Box type, default `bx11` |

The switches — `network-policy:enabled`, `cluster-services:acmeStaging` and
`cluster-services:kedaEnabled` — take `true` or `false` and refuse anything
else, including YAML's own `yes` and `on`, rather than reading it as a silent
`false`. For `acmeStaging` a dropped value would send the order to Let's
Encrypt's production endpoint.

Layer config is about what a layer deploys, not about the cluster. The domain
is not a stack config key: `50-gitops` reads `metadata.domain` from the cluster
tier, so `40-ingress` and `50-gitops` cannot spell one hostname two ways — see
[domain.md](domain.md#one-name-spelled-once).

## State, and where the token lives

State lives in **Pulumi Cloud**, declared as `backend:` in every `Pulumi.yaml`
so it is a property of the repository rather than of whoever last ran
`pulumi login`.

`task cluster:token` writes the Hetzner token into `Pulumi.<stack>.yaml` as a
`secure:` ciphertext. It takes no `token=` argument: a credential passed as one
lands in the shell history and in the process arguments, where `ps` shows it.
Pulumi prompts for the value with the input hidden, or reads standard input
when something is piped:

```bash
pass hetzner/token | task cluster:token
```

`Pulumi.<stack>.yaml` is gitignored. The ciphertext is safe to publish — the
key is the backend's — but the file is one operator's environment: their
token, their org, the stack their layers point at. So a fresh clone runs
`task cluster:token` and `task platform:init`, which write those files locally.
Nothing here reads a plaintext secrets file, and none should be created.

An exported `HCLOUD_TOKEN` takes priority over the stack config, which is how
CI passes a token it holds as a GitHub secret.

## Secrets from Pulumi ESC

The External Secrets Operator ships with `30-cluster-services` and reads
nothing until a token exists. Set one and it creates a `ClusterSecretStore`
named `pulumi-esc` pointing at `<org>/<project>/<stack>` — one environment per
stack, so `prod` cannot read `dev`:

```bash
pulumi config set --secret cluster-services:pulumiAccessToken <token>
task platform:apply stack=dev layer=30-cluster-services
```

The organization and stack are the ones the program runs as, and the project is
this repository's own name; none of them is configured. Use an
**organization** token scoped to reading those environments, not a personal
one — it is copied into a Kubernetes Secret, so anything able to read that
Secret can do whatever the token can.

What belongs there is a secret a **workload** reads. What Pulumi needs to build
the cluster — the Hetzner token, the Talos bundle, the Storage Box passwords —
stays in stack config and state, because it is needed before a cluster exists.

`task cluster:smoke` reports whether the store is ready. A store with a bad
token stops every `ExternalSecret` from syncing, and the Secret it would have
written is **absent** rather than stale — so the pod that mounts it fails to
start with a message about a Secret, not about the token.

Export the environment with the rest of what lives nowhere else:

```bash
pulumi env get <org>/<project>/<stack> --value json
```

## Keeping state in your own S3 bucket

Pulumi calls this a DIY backend: it stores state under a `.pulumi` directory in
the bucket, and backing it up and coordinating access across a team becomes
yours to do.

Hetzner Object Storage is S3-compatible, so the form is the one Pulumi
documents for any S3-compatible server — `endpoint`, `s3ForcePathStyle` and,
for a plain-HTTP server such as a local Minio, `disableSSL`:

```bash
# Credentials and region reach the AWS SDK through its own environment, not
# through the URL: the bucket's Object Storage keys, and a region the SDK
# will not proceed without.
export AWS_ACCESS_KEY_ID=...
export AWS_SECRET_ACCESS_KEY=...
export AWS_REGION=fsn1

pulumi login 's3://<bucket>?endpoint=fsn1.your-objectstorage.com&s3ForcePathStyle=true'
```

`PULUMI_BACKEND_URL` holds the same URL if you would rather not log in. To make
the bucket the default for everyone, point the `backend:` in every
`Pulumi.yaml` at it.

The bucket has to exist first: a bucket managed by the state it holds cannot
create itself. Enable versioning on it — state is the one file whose loss
cannot be recovered from the cloud it describes, and a half-written checkpoint
looks like a correct one until the next apply.

### One rule changes with it

A DIY backend has no key-management service behind it, so stack secrets are
encrypted with a passphrase you supply:

```bash
export PULUMI_CONFIG_PASSPHRASE=...
```

That makes the `secure:` ciphertext in `Pulumi.<stack>.yaml` offline-
attackable — anyone with the file can grind the passphrase. **Never publish
those files on a DIY backend with a passphrase.**

The ciphertext is safe to publish on Pulumi Cloud only because the service
holds the key. To get the same property with your own bucket, use a cloud KMS
as the secrets provider instead of a passphrase — the CLI documents
`awskms://`, `azurekeyvault://` and `gcpkms://` for
`pulumi stack init --secrets-provider`, and those work with any backend.
