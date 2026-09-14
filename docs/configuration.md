# Configuration

Three places hold configuration, and the split is deliberate.

| Where | Holds |
|-------|-------|
| `infra/cluster/cluster.<stack>.yaml` | everything that shapes a cluster; not committed |
| Pulumi stack config | the Hetzner token, and what each layer deploys |
| `pkg/charts` | every chart version |

## The topology file

Everything that shapes a cluster lives in
`infra/cluster/cluster.<stack>.yaml`, validated by `cluster.schema.json` as you
type it and by the same Go code the Pulumi program runs when you apply.

It is sparse: anything omitted keeps the default in `pkg/hetzner`. Start from
[cluster.example.yaml](../infra/cluster/cluster.example.yaml), which is
committed with an RFC 5737 placeholder in
`network.adminCIDRs` — set that to the address you will apply from, because
Talos configuration is pushed over the Talos API and a host outside the list
hangs with the port filtered.

The stack files themselves are **not** committed, for that one field.

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

Switching the field means re-baking: `task cluster:image-bake`. A snapshot is
one architecture, and the bake scopes its "already baked?" question to the
architecture the topology asks for — so switching finds no snapshot and bakes
one, rather than finding the other architecture's and doing nothing.

**Arm is not the cheaper option on this provider.** From the API in `hel1` on
2026-09-14, monthly gross:

| | cores | memory | arch | EUR/month |
|---|---|---|---|---|
| `cx23` | 2 | 4 GB | x86 | 5.49 |
| `cax11` | 2 | 4 GB | arm | 5.99 |
| `cx33` | 4 | 8 GB | x86 | 8.49 |
| `cax21` | 4 | 8 GB | arm | 10.49 |

Every image the platform installs publishes `linux/arm64` at the versions
pinned in [`pkg/charts`](../pkg/charts) — Cilium, both hcloud drivers,
cert-manager, external-secrets, metrics-server, Traefik and Argo CD, checked
on 2026-09-14. So nothing in the platform is the obstacle; the reason to pick
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
per-type availability, and `/v1/datacenters` has been deprecated since
2025-12-16. **It needs a support ticket, not a wait for capacity.**

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
`pkg/hetzner.BuildFirewallRules` refuses an empty `network.adminCIDRs` — that is
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
| `hcloud:token` | [`infra/cluster`](../infra/cluster) | Hetzner API token (secret); `cluster:image-bake` decrypts it from here too |
| `<layer>:clusterStackRef` | every [layer](../layers) | `<org>/hetzner-cluster/<stack>`; written by `platform:init` |
| `node-platform:hcloudToken` | [`10-node-platform`](../layers/10-node-platform) | optional; overrides the token the cluster stack exports (secret) |
| `network-policy:enabled` | [`20-network-policy`](../layers/20-network-policy) | create the policies; `false` by default, see [design.md](design.md#the-default-deny-is-opt-in) |
| `cluster-services:acmeEmail` | [`30-cluster-services`](../layers/30-cluster-services) | enables the Let's Encrypt ClusterIssuer; omit it and none is created |
| `ingress:loadBalancerType` | [`40-ingress`](../layers/40-ingress) | Hetzner load balancer type, default `lb11` |
| `gitops:domain` | [`50-gitops`](../layers/50-gitops) | publishes Argo CD through ingress; omit it and there is no Ingress |

Layer config is about what a layer deploys, not about the cluster. The three
cluster switches that used to sit here are in the topology now.

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
