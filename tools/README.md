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
| [stack/](stack) | list the cluster tier's environments |
| [stackstatus/](stackstatus) | every stack of one environment: last run, interrupted updates, and whether they agree on the cluster |
| [stackdrift/](stackdrift) | every resource of one environment the cloud no longer agrees with, from refresh previews |
| [talos/](talos) | does Talos itself accept the machine-config patches |
| [taskshell/](taskshell) | shellcheck over the shell inside the taskfiles |
| [token/](token) | the Hetzner token for a stack, on stdout |
| [topology/](topology) | validate every committed topology, and read one field out of one |

Each is a tool rather than a shell pipeline because a tool has tests beside it.
The repository's own gates are not here; they are
[internal/ci](../internal/ci), test files with nothing to run.
