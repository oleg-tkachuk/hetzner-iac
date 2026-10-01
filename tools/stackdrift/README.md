# stackdrift

Reports every resource the cloud no longer agrees with, for every tier's and
every layer's stack in one environment.

```bash
go run ./tools/stackdrift --stack dev infra/cluster infra/backup layers/10-node-platform …
```

`task platform:drift stack=dev` runs it over every project, in order.

`pulumi preview` cannot answer this. It compares the program with the state,
and the state is what the last apply wrote, so a label edited in the Hetzner
console or a field patched with `kubectl` leaves a preview with nothing to
say. Only a refresh reads the cloud. This runs each stack's refresh *preview*
through the Automation API, which reads every resource from its provider and
writes nothing back.

Per resource that differs: whether it changed or is gone, and which fields.
Fields another tool added are not drift — the Kubernetes provider applies
server-side and compares only the fields it manages — so a `kubectl label`
on a managed object is not reported, and a `kubectl patch` of a field Pulumi
set is.

It exits 1 when anything drifted, and when a stack could not be read: a report
that cannot say "no drift" does not exit as though it had. A refresh adopts the
cloud's version into the state; an apply puts the code's back.

The judgements and the rendering live in
[internal/pkg/stackdrift](../../internal/pkg/stackdrift), which has no Pulumi
import.
