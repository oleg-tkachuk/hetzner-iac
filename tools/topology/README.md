# topology

Validates every committed cluster topology, and reads one field out of one.

```bash
go run ./tools/topology <dir>                        # validate every cluster.*.yaml in it
go run ./tools/topology get <field> <topology.yaml>  # print one value
```

| Field | Is |
|-------|-----|
| `cluster-name` | `metadata.name` |
| `location` | `placement.location` |
| `talos-version` | `talos.version` |
| `talos-architecture` | `talos.architecture` |
| `kubernetes-version` | `kubernetes.version` |

Validation is the same code the Pulumi program runs —
`pkg/hetzner.LoadTopology` — so the check cannot drift from the thing it
checks. A malformed cluster description then fails in CI in a second, instead
of at `pulumi up` after credentials have been set up and a preview waited for.

`get` exists because the shell alternative does not work.
`grep -A3 '^talos:' | grep version: | head -1 | awk '{print $2}'` reads three
lines after a key and hopes the field is among them, so it is right only while
nobody writes a comment. Three of the four call sites this replaced were
returning an empty string — the Talos version in prod and the Kubernetes
version in both — silently, because the comments above those fields had grown
past the window.

It refuses to print an empty field. A caller substituting an empty string into
a URL or an image selector builds something plausible and wrong, and every
field here is either required or defaulted, so empty means the topology is not
what the caller thinks it is.

Run by `task cluster:*` for the cluster name and versions, by the pre-commit
hook, and by CI.
