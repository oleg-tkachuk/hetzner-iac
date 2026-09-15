# talos

Checks the machine-config patches against Talos itself: it generates a
baseline configuration for the pinned version, applies the patches this
repository produces, and runs `talosctl validate`.

```bash
go run ./tools/talos [dir]   # default: infra/cluster
```

The unit tests in [internal/pkg/hetzner](../../internal/pkg/hetzner) prove the patches contain
what was intended. They cannot prove Talos accepts them, and Talos is strict in
ways that are not guessable. The first run found that
`machine.network.hostname` is rejected outright, because a HostnameConfig
document is always present and setting the name in both places is a conflict —
that would have failed at apply, after the servers existed and were being paid
for.

**The `talosctl` in PATH must match the version the topology pins.** Talos
moves configuration between documents across minor versions, so validating a
1.13 config with a 1.14 binary reports conflicts that do not exist, and the
other way round misses real ones.

No cluster and no credentials: it validates offline.

Run by `task cluster:machine-config:check` and by CI.
