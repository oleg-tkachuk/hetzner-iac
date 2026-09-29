# stackstatus

Reports every tier's and every layer's Pulumi stack for one environment.

```bash
go run ./tools/stackstatus --stack dev infra/cluster infra/backup layers/10-node-platform …
```

Per stack: the last operation and its result, when and how long it ran, what
it changed, how many resources the stack holds, and the commit it was applied
from. Beyond that, what no single `pulumi stack` says:

- **an interrupted checkpoint** — pending operations, resources awaiting
  deletion or replacement, tainted ones, ones with init errors;
- **a reference elsewhere** — a `clusterStackRef` that does not end in this
  environment's `<cluster project>/<stack>`;
- **contract skew** — a consumer applied against a `contractVersion` the
  cluster no longer publishes, or a cluster behind the version this checkout
  reads.

Everything comes through the Automation API. Outputs come from the exported
checkpoint rather than `StackOutputs`, so secrets stay ciphertext. Reading the
history has to select the stack, so the workspace's previous selection is put
back afterwards.

The judgements and the table live in
[internal/pkg/stackstatus](../../internal/pkg/stackstatus), which has no
Pulumi import. Run by `task platform:status`, which passes the tiers and then
LAYERS in order.
