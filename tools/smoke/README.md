# smoke

Asks whether a cluster this repository built can actually run a workload: nodes
Ready, a volume obtainable, a load balancer reachable — and whether the
platform's layers hold, from network policies to image admission.
[operations.md](../../docs/operations.md#does-the-cluster-actually-work)
lists every check.

```bash
go run ./tools/smoke --kubeconfig ./kubeconfig
```

| Flag | Default |
|------|---------|
| `--kubeconfig` | `./kubeconfig`; empty uses the standard rules, which respect `KUBECONFIG` |
| `--context` | the kubeconfig's own current-context |
| `--storage-class` | the platform's class, for the probe claim |
| `--bind-timeout` | how long to wait for the probe claim to reach Bound |
| `-v` | print each probe as it is applied, and the API server's warnings |

Exits 2 when the checks ran and the cluster failed them, 1 when they could not
run at all. `go run` and `task` both report any failure as 1, so a caller that
needs the difference builds the binary.

It exists because `pulumi up` going green is not the same claim: every resource
can be created and every pod Running while no volume is obtainable.

A thin shell around [internal/pkg/clustersmoke](../../internal/pkg/clustersmoke), where the
judgements and the client-go calls live and are tested.

Run by `task cluster:smoke`.
