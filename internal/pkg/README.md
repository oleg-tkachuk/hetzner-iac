# pkg

The implementation: the Pulumi components, contracts and data every program
under [layers/](../../layers), [infra/cluster/](../../infra/cluster) and
[tools/](../../tools) is built from.

| Package | Holds |
|---------|-------|
| [hetzner/](hetzner) | the cluster tier's resources and the topology that validates into them |
| [layer/](layer) | the shim every layer shares: cluster resolution, provider, components, Helm |
| [clusterref/](clusterref) | the versioned output contract between the cluster tier and the layers |
| [charts/](charts) | every chart version, pinned, in one table |
| [values/](values) | every chart's Helm values, as templates |

| [workloads/](workloads) | what each chart is expected to produce, which the render check and the e2e suite both assert |
| [platform/](platform) | the names two layers must spell identically: storage class, ingress class, node ports |
| [cni/](cni) | which CNI a stack installs, and whether it agrees with what Talos did to kube-proxy |
| [clustersmoke/](clustersmoke) | can a cluster run a workload — the judgements behind `tools/smoke` |
| [pulumilog/](pulumilog) | the output vocabulary, the same one the taskfiles print |
| [pulumiopts/](pulumiopts) | one function, because appending to a shared option slice writes into the caller's array |

Under `internal/` because nothing outside this module imports any of it, and
now nothing can: Go refuses an import of `.../internal/...` from outside the
tree. That was already true in practice — the releases are releases of an
infrastructure tree, and no released version of this module even resolves for
`go get`, since the path carries no `/vN` suffix while the tags are long past
v1. The move makes it a property of the compiler rather than a fact about the
world.

Grouped under `pkg/` rather than sitting directly in `internal/`, and the
grouping is the point: these are the IaC implementation, while
[ci/](../ci) beside them is the repository's own gates — tests about
workflows, taskfiles and documentation, with nothing to run and nothing to
import. Two different kinds of thing, two directories.
