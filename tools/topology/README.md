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
`internal/pkg/clusterspec.LoadTopology` — so the check cannot drift from the
thing it checks, and a malformed topology fails in CI instead of at
`pulumi up`.

`get` exists so shell callers ask the YAML parser rather than grepping a fixed
number of lines after a key, which returns empty once a comment grows past the
window. It refuses to print an empty field: every field here is required or
defaulted, so empty means the topology is not what the caller thinks it is.

Run by `task cluster:*` for the cluster name and versions, by the pre-commit
hook, and by CI.
