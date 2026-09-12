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
| `hcloud:token` | `infra/cluster` | Hetzner API token (secret); `cluster:image-bake` decrypts it from here too |
| `<layer>:clusterStackRef` | every layer | `<org>/hetzner-cluster/<stack>` |
| `node-platform:hcloudToken` | `10-node-platform` | optional; overrides the token the cluster stack exports (secret) |
| `core:acmeEmail` | `30-core` | enables the Let's Encrypt ClusterIssuer; omit it and none is created |
| `ingress:loadBalancerType` | `40-ingress` | Hetzner load balancer type, default `lb11` |
| `gitops:domain` | `50-gitops` | publishes Argo CD through ingress; omit it and there is no Ingress |
| `observability:metricsRetention` | `60-observability` | default `30d` |
| `observability:metricsVolumeSize` | `60-observability` | default `50Gi` |

Layer config is about what a layer deploys, not about the cluster. The three
cluster switches that used to sit here are in the topology now.

## State, and why the token is committed

State lives in **Pulumi Cloud**, declared as `backend:` in every `Pulumi.yaml`
so it is a property of the repository rather than of whoever last ran
`pulumi login`.

`pulumi config set --secret` writes the Hetzner token into
`Pulumi.<stack>.yaml` as a `secure:` ciphertext, and those files are committed
on purpose: the plaintext is recoverable only with the stack's key, which the
backend holds and the repository does not. So the token is versioned with the
code it configures, and cloning the repository grants nothing. Nothing here
reads a plaintext secrets file, and none should be created.

An exported `HCLOUD_TOKEN` takes priority over the stack config, which is how
CI passes a token it holds as a GitHub secret.

## Keeping state at Hetzner instead

One vendor, data in the EU. Override the URL — it is the only change needed:

```bash
export PULUMI_BACKEND_URL='s3://<bucket>?endpoint=fsn1.your-objectstorage.com&s3ForcePathStyle=true&region=fsn1'
export AWS_ACCESS_KEY_ID=...
export AWS_SECRET_ACCESS_KEY=...
export PULUMI_CONFIG_PASSPHRASE=...
```

The bucket has to exist first: a bucket managed by the state it holds cannot
create itself.

One consequence changes a rule above. A self-managed backend encrypts stack
secrets with that passphrase rather than with a key a service holds, so the
`secure:` ciphertext in `Pulumi.<stack>.yaml` becomes offline-attackable —
**do not commit those files on a self-managed backend in a public repository.**
Pulumi Cloud manages the key, which is what makes committing them safe here.
