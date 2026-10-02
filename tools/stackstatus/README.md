# stackstatus

Reports every tier's and every layer's Pulumi stack for one environment.

```bash
go run ./tools/stackstatus --stack dev infra/cluster infra/backup layers/10-node-platform …
```

Per stack: the last operation and its result, when and how long it ran, what
it changed, how many resources the stack holds, and the commit it was applied
from. Beyond that, what no single `pulumi stack` says:

- **an interrupted checkpoint** — pending operations, resources awaiting
  deletion or replacement, tainted ones, ones with init errors — and, under
  the table, the `refresh` that clears each pending operation;
- **a reference elsewhere** — a `clusterStackRef` that does not end in this
  environment's `<cluster project>/<stack>`;
- **contract skew** — a consumer applied against a `contractVersion` the
  cluster no longer publishes, or a cluster behind the version this checkout
  reads.

It refuses to start when this machine holds no credential for the projects'
backend, and names the command with that backend's URL —
`pulumi login https://api.pulumi.com` for the one every project declares. The CLI does not fail on its own when an AI
coding agent runs it: it signs up a temporary agent account, and the report
would describe that account's empty backend. The check reads the credentials
store through the SDK, before the first CLI call.

Everything else comes through the Automation API. Outputs come from the exported
checkpoint. The export decrypts secrets, as `StackOutputs` would, but keeps each
in its envelope, and the report prints only bare strings. Reading the
history has to select the stack, so the workspace's previous selection is put
back afterwards.

The judgements and the table live in
[internal/pkg/stackstatus](../../internal/pkg/stackstatus), which has no
Pulumi import. Run by `task platform:status`, which passes the tiers and then
LAYERS in order.
