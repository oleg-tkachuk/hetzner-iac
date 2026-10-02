# talos

Checks the machine-config patches against Talos itself: it generates a
baseline configuration for the pinned version, applies the patches this
repository produces, and runs `talosctl validate`.

```bash
go run ./tools/talos [dir]   # default: infra/cluster
```

The unit tests in [internal/pkg/clusterspec](../../internal/pkg/clusterspec) prove the patches
contain what was intended. They cannot prove Talos accepts them, and a patch
Talos rejects otherwise fails at apply, after the servers exist.

A worker is validated with the cluster patch and the node patch of each of
its pools — labels, taints, hostname — and with a probe pool when the topology
has none.

**The `talosctl` in PATH must match the version the topology pins.** Talos
moves configuration between documents across minor versions, so a mismatched
binary reports conflicts that do not exist, or misses real ones.

No cluster and no credentials: it validates offline.

Run by `task cluster:machine-config:check` and by CI.
