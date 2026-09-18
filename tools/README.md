# tools

The small commands this repository runs from its taskfiles, its git hooks and
CI. One directory per command, each named for its subject rather than its
implementation.

| Tool | Answers |
|------|---------|
| [charts/](charts) | what each chart pin is, whether it is behind upstream, and whether it still renders |
| [etcd/](etcd) | is this file really an etcd snapshot, and how far had it applied |
| [golangci/](golangci) | run golangci-lint at the version CI pins, and refuse another |
| [image/](image) | bake the Talos snapshot the topology names, idempotently |
| [lines/](lines) | the line-oriented parsing, one awk op per question |
| [orphans/](orphans) | which Hetzner resources nothing claims any more |
| [recoverykit/](recoverykit) | the parts of a cluster that live nowhere but Pulumi's state |
| [secrets/](secrets) | print a stack's Talos secrets bundle |
| [smoke/](smoke) | can this cluster actually run a workload |
| [stack/](stack) | does a Pulumi stack exist — and make sure it does |
| [talos/](talos) | does Talos itself accept the machine-config patches |
| [taskshell/](taskshell) | shellcheck over the shell inside the taskfiles |
| [token/](token) | the Hetzner token for a stack, on stdout |
| [topology/](topology) | validate every committed topology, and read one field out of one |

Several of them replaced a shell pipeline that was wrong in a way nothing
reported: a `grep -A3` that returned empty once a comment grew past its window,
a `file | grep` that knew two binary formats out of many, an `init || select`
that hid the real error behind a misleading one. The rest exist because
something went green while being broken. Either way the reason is the same: a
tool has tests beside it, and a pipeline has none.

The repository's own gates are not here. They are
[internal/ci](../internal/ci), because they are test files with nothing to run.
