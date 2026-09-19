# Configuration

Three places hold the configuration of a DEPLOYMENT, and the split is
deliberate.

| Where | Holds |
|-------|-------|
| `infra/cluster/cluster.<stack>.yaml` | everything that shapes a cluster; not committed |
| Pulumi stack config | the Hetzner token, and what each layer deploys |
| `internal/pkg/charts` | every chart version |

## Where configuration lives

Everything configurable in this repository, and what it decides. Read this to
find the file; read the sections below for what its fields mean.

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
| [internal/pkg/charts](../internal/pkg/charts) | every chart's version, repository and namespace: the pins |
| [internal/pkg/values](../internal/pkg/values) | one Helm values template per chart, which is what actually reaches Helm |
| [internal/pkg/clusterspec](../internal/pkg/clusterspec) | the topology's schema in Go, its defaults, and the Talos machine-config patches |
| [internal/pkg/clusterspec/auditpolicy.yaml](../internal/pkg/clusterspec/auditpolicy.yaml) | what the API server audit log records |
| [internal/pkg/platform](../internal/pkg/platform) | names two layers must agree on: storage classes, the ingress class, the issuer, node ports |
| [internal/pkg/chartsettings](../internal/pkg/chartsettings) | the chart value KEYS this repository sets, and what each one prevents |
| [internal/pkg/clusterref](../internal/pkg/clusterref) | the cluster tier's stack outputs, by name — the contract every layer reads |
| [layers/20-network-policy/manifests](../layers/20-network-policy/manifests) | the cluster-wide network policy, one file per rule |
| [layers/30-cluster-services/manifests](../layers/30-cluster-services/manifests) | the manifests that layer applies alongside its charts |

**The repository — how it runs and what it refuses**

| File | Configures |
|------|------------|
| [Taskfile.yaml](../Taskfile.yaml) | the cluster commands, and the layer order `layer=all` walks |
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

It is sparse: anything omitted keeps the default in `internal/pkg/clusterspec`. Start from
[cluster.example.yaml](../infra/cluster/cluster.example.yaml), which is
committed with an RFC 5737 placeholder in
`network.adminCIDRs` — set that to the address you will apply from, because
Talos configuration is pushed over the Talos API and a host outside the list
hangs with the port filtered.

The stack files themselves are **not** committed, for that one field.

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
traffic breaks while they do. It is one value and no Talos apply, because the
gateway route goes into the machine config in both modes — under `tunnel` it is
never used, Cilium's own routes being more specific.

`docs/design.md` has the mechanism, and the failure that made this a field
rather than a constant: `autoDirectNodeRoutes` cannot work on a Hetzner private
network in either mode, and while it was set, pod-to-pod traffic across nodes
had no route at all.

## CPU architecture

`talos.architecture` is `x86` or `arm`, and one field decides three things:

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
one architecture, and the bake scopes its "already baked?" question to the
architecture the topology asks for — so switching finds no snapshot and bakes
one, rather than finding the other architecture's and doing nothing.

**Arm is not the cheaper option on this provider.** From the API in `hel1`,
monthly gross:

| | cores | memory | arch | EUR/month |
|---|---|---|---|---|
| `cx23` | 2 | 4 GB | x86 | 5.49 |
| `cax11` | 2 | 4 GB | arm | 5.99 |
| `cx33` | 4 | 8 GB | x86 | 8.49 |
| `cax21` | 4 | 8 GB | arm | 10.49 |

Every image the platform installs publishes `linux/arm64` at the versions
pinned in [`internal/pkg/charts`](../internal/pkg/charts) — Cilium, both hcloud drivers,
cert-manager, external-secrets, metrics-server, Traefik and Argo CD, all
checked. So nothing in the platform is the obstacle; the reason to pick
`arm` is wanting Arm nodes, not saving money.

### Which locations have Arm, and why that is not the blocker here

Arm is regional. Hetzner's own
[locations table](https://docs.hetzner.com/cloud/general/locations/) lists
**Cloud Shared `AMPERE`** — the `cax` line — in three locations only:

| | `fsn1` | `nbg1` | `hel1` | `ash` | `hil` | `sin` |
|---|---|---|---|---|---|---|
| Cloud Shared `AMPERE` | ✓ | ✓ | ✓ | ✗ | ✗ | ✗ |

So an Arm topology has to sit in Falkenstein, Nuremberg or Helsinki. Anything in
`ash`, `hil` or `sin` cannot be Arm at all, and that is a property of the
location rather than of an account.

The live API agrees about the region and then refuses anyway. It reports `cax11`
as *available* in `hel1-dc2` and `nbg1-dc3`, and *supported but not available*
in `fsn1-dc14` — yet every create in all three is refused:

```
unsupported location for server type (invalid_input)
```

That is not a mismatched image and not the CLI: it comes back the same from the
raw API with an Arm image by id. Nor is it the location or the project's
standing, which one pair of requests settles — same project, same location, same
request shape:

```
cx23  nbg1  CREATED
cax11 nbg1  unsupported location for server type
```

So the refusal follows the architecture, not the region. Hetzner's API is
contradicting its own availability data, and nothing outside the account can
say whether that is an entitlement or a defect — `/v1/locations` carries no
per-type availability, and `/v1/datacenters` has been deprecated since 2025-12-16.
**It needs a support ticket, not a wait for capacity.**

Before planning an Arm cluster, probe one server by hand — an Arm image by id,
not by name, because Hetzner answers an x86 image on an Arm type with the *same*
message and a name resolves to whichever architecture the CLI picks:

```bash
hcloud image list --type system --architecture arm -o columns=id,name
hcloud server create --name arch-probe --type cax11 --image <id> --location hel1
```

Delete the probe if it succeeds — a server costs from the moment it exists.

The Arm **dedicated** line is a different product and out of scope here: RX170
and RX220 are Ampere Altra machines on Hetzner's robot side, not the Cloud API
this repository provisions through.

## What the policy pack enforces

Component validation binds only the callers that go through the component, and
this repository has a measured example of the gap.
`internal/pkg/clusterspec.BuildFirewallRules` refuses an empty `network.adminCIDRs` — that is
what `ErrEmptyAdminCIDRs` is for — and then appends `FirewallRuleOptions.Extra`
verbatim. Its per-rule check looks at the protocol, the port, and that the
source list is non-empty. It never looks at what the sources *are*, so an extra
rule opening `6443` to `0.0.0.0/0` is accepted by the validator whose entire
purpose is to refuse one.

[`policy/`](../policy) closes that with CrossGuard, which runs over the
resources a program declares rather than the constructors it called:

| Policy | Refuses |
|---|---|
| `hcloud-admin-ports-not-world-open` | `6443` or `50000` reachable from `0.0.0.0/0` or `::/0` |
| `hcloud-server-joins-private-network` | a server with no network attachment — etcd and kubelet ride `network.nodeSubnet` |
| `helm-release-pins-chart-version` | a release that resolves to whatever the repository serves today |

```bash
task policy:check stack=dev
```

It previews the cluster tier and every layer, changes nothing, and is written
in Go so CI needs no Node or Python runtime for it.

### It is a gate, not yet a control

`pulumi preview --policy-pack` is **local** enforcement: a policy is skipped by
omitting the flag, so this catches mistakes rather than preventing them.

Unlike a `file://` backend, this repository's state is in Pulumi Cloud, so the
mode that cannot be skipped *is* available — organisation policy groups, which
apply to every stack in the organisation with no flag to forget. Turning that on
is an account setting rather than a repository change, which is why the pack
ships as a task here and the decision stays with whoever owns the organisation.

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
| `ingress:loadBalancerType` | [`40-ingress`](../layers/40-ingress) | Hetzner load balancer type, default `lb11` |
| `gitops:repoURL` | [`50-gitops`](../layers/50-gitops) | the repository Argo CD reconciles; omit it and Argo CD is installed and reconciles nothing |
| `gitops:path` | [`50-gitops`](../layers/50-gitops) | where the tree of Applications starts in that repository, default the root |
| `gitops:revision` | [`50-gitops`](../layers/50-gitops) | branch, tag or commit to track, default `HEAD` |
| `gitops:repoUsername` | [`50-gitops`](../layers/50-gitops) | HTTPS username for a private repository; any non-empty string when the password is a token (secret) |
| `gitops:repoPassword` | [`50-gitops`](../layers/50-gitops) | HTTPS password or token for a private repository (secret) |
| `gitops:repoSSHPrivateKey` | [`50-gitops`](../layers/50-gitops) | SSH private key for a private repository, in place of the two above — a read-only deploy key is enough (secret) |
| `backup:storageBoxType` | [`backup`](../infra/backup) | Hetzner Storage Box type, default `bx11` |

Layer config is about what a layer deploys, not about the cluster. The three
cluster switches that used to sit here are in the topology now.

The **domain** is one of them, and it used to be listed above as
`gitops:domain`. It never was a stack config key: `50-gitops` reads
`metadata.domain` from the cluster tier, because `40-ingress` points DNS at its
load balancer and this layer gives Argo CD a hostname — two copies of one name
drift, and an Ingress for one name behind a record for another is accepted by
everything and serves nothing. `TestConfigKeys_TheTablesNameKeysThatExist`
holds these tables to the keys the layers actually declare, which is what a
documented key nothing reads escaped before. What the domain is and what it
needs is [domain.md](domain.md).

## State, and where the token lives

State lives in **Pulumi Cloud**, declared as `backend:` in every `Pulumi.yaml`
so it is a property of the repository rather than of whoever last ran
`pulumi login`.

`task cluster:token` writes the Hetzner token into `Pulumi.<stack>.yaml` as a
`secure:` ciphertext. It takes no `token=` argument on purpose: a credential
passed as one lands in the shell history and in the process arguments, where
`ps` shows it to every other user on the machine. Pulumi prompts for a value
that is not on the command line and hides the input, and reads standard input
when something is piped — so `pass hetzner/token | task cluster:token` works
without the value ever reaching argv.

`Pulumi.<stack>.yaml` is gitignored. The ciphertext is safe to publish — the
key is the backend's, not the repository's — but the file is one operator's
environment: their token, their org, the stack their layers point at. A public
repository should not ship it, and a clone should not start by pointing at
somebody else's cluster.

So a fresh clone runs `task cluster:token` and `task platform:init`, which
write those files locally. Nothing here reads a plaintext secrets file, and
none should be created.

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

Neither the organization nor the stack is configured: both are what the program
is already running as, and the project is this repository's own name. An
**organization** token scoped to reading those environments, not a personal
one — it is copied into a Kubernetes Secret, so whatever it can do, anything
able to read that Secret can do.

What belongs there is a secret a **workload** reads. What Pulumi needs to build
the cluster — the Hetzner token, the Talos bundle, the Storage Box passwords —
stays in stack config and state, because it is needed before a cluster exists.

`task cluster:smoke` reports whether the store is ready. It matters because the
failure is quiet: a store with a bad token stops every `ExternalSecret` from
syncing, and the Secret it would have written is **absent** rather than stale —
so the pod that mounts it fails to start with a message about a Secret, three
steps from the token that is wrong.

The environment itself is worth exporting with the rest of what lives nowhere
else:

```bash
pulumi env get <org>/<project>/<stack> --value json
```

## Keeping state in your own S3 bucket

One vendor, one bill, and no Pulumi account. Pulumi calls this a DIY
backend: it stores state under a `.pulumi` directory in the bucket, and
backing it up and coordinating access across a team becomes yours to do.

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

`PULUMI_BACKEND_URL` holds the same URL if you would rather not log in, and
every `Pulumi.yaml` here declares `backend:` so the choice is a property of
the repository rather than of whoever last ran `pulumi login` — point that at
the bucket to make it the default for everyone.

The bucket has to exist first: a bucket managed by the state it holds cannot
create itself. Enable versioning on it. State is the one file whose loss
cannot be recovered from the cloud it describes, and a half-written checkpoint
is indistinguishable from a correct one until the next apply.

### One rule changes with it

A DIY backend has no key-management service behind it, so stack secrets are
encrypted with a passphrase you supply:

```bash
export PULUMI_CONFIG_PASSPHRASE=...
```

That makes the `secure:` ciphertext in `Pulumi.<stack>.yaml` offline-
attackable — anyone with the file can grind the passphrase. **Do not commit
those files on a DIY backend in a public repository.** Add them to
`.gitignore` and keep the secrets somewhere else.

Committing them is safe *here* only because Pulumi Cloud holds the key. If you
would rather keep the files committed and the state in your own bucket, use a
cloud KMS as the secrets provider instead of a passphrase — the CLI documents
`awskms://`, `azurekeyvault://` and `gcpkms://` for
`pulumi stack init --secrets-provider`, and those work with any backend.
