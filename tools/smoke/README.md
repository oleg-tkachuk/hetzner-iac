# smoke

Asks whether a cluster this repository built can actually run a workload, and
exits non-zero when it cannot: nodes Ready, a volume obtainable, a load
balancer reachable.

```bash
go run ./tools/smoke --kubeconfig ./kubeconfig
```

| Flag | Default |
|------|---------|
| `--kubeconfig` | `./kubeconfig`; empty uses the standard rules, which respect `KUBECONFIG` |
| `--context` | the kubeconfig's own current-context |
| `--storage-class` | the platform's class, for the probe claim |
| `--bind-timeout` | how long to wait for the probe claim to reach Bound |

It exists because `pulumi up` going green is not the same claim. It went green
on a three-node cluster whose hcloud CSI controller was in CrashLoopBackOff:
every resource created, every pod Running, and no volume obtainable. Nothing
said so, because nothing asked for one.

A thin shell around [pkg/clustersmoke](../../pkg/clustersmoke), where the
judgements and the client-go calls live and are tested. What is here is
argument handling and the report's shape.

Run by `task cluster:smoke`.
