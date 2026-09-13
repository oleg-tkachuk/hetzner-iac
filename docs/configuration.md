# Configuration

Three places hold configuration, and the split is deliberate.

| Where | Holds |
|-------|-------|
| `infra/cluster/cluster.<stack>.yaml` | everything that shapes a cluster |
| Pulumi stack config | the Hetzner token, and what each layer deploys |
| `pkg/charts` | every chart version |

## The topology file

Everything that shapes a cluster lives in
`infra/cluster/cluster.<stack>.yaml`, validated by `cluster.schema.json` as you
type it and by the same Go code the Pulumi program runs when you apply.

It is sparse: anything omitted keeps the default in `pkg/hetzner`. Start from
`cluster.example.yaml`, which is committed with an RFC 5737 placeholder in
`network.adminCIDRs` — set that to the address you will apply from, because
Talos configuration is pushed over the Talos API and a host outside the list
hangs with the port filtered.

The stack files themselves are **not** committed, for that one field.

## Stack config

| Key | Where | Meaning |
|-----|-------|---------|
| `hcloud:token` | [`infra/cluster`](../infra/cluster) | Hetzner API token (secret); `cluster:image-bake` decrypts it from here too |
| `<layer>:clusterStackRef` | every [layer](../layers) | `<org>/hetzner-cluster/<stack>` |
| `node-platform:hcloudToken` | [`10-node-platform`](../layers/10-node-platform) | optional; overrides the token the cluster stack exports (secret) |
| `network-policy:enabled` | [`20-network-policy`](../layers/20-network-policy) | create the policies; `false` by default, see [design.md](design.md#the-default-deny-is-opt-in) |
| `core:acmeEmail` | [`30-core`](../layers/30-core) | enables the Let's Encrypt ClusterIssuer; omit it and none is created |
| `ingress:loadBalancerType` | [`40-ingress`](../layers/40-ingress) | Hetzner load balancer type, default `lb11` |
| `gitops:domain` | [`50-gitops`](../layers/50-gitops) | publishes Argo CD through ingress; omit it and there is no Ingress |

Layer config is about what a layer deploys, not about the cluster. The three
cluster switches that used to sit here are in the topology now.

## State, and why the token is committed

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

Those files are committed on purpose: the plaintext is recoverable only with
the stack's key, which the backend holds and the repository does not. So the
token is versioned with the code it configures, and cloning the repository
grants nothing. Nothing here reads a plaintext secrets file, and none should
be created.

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
