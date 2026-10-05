# pkg

The implementation: the Pulumi components, contracts and data every program
under [layers/](../../layers), [infra/cluster/](../../infra/cluster) and
[tools/](../../tools) is built from.

| Package | Holds |
|---------|-------|
| [hetzner/](hetzner) | the cluster tier's Pulumi resources: network, firewall, control plane, worker pools |
| [clusterspec/](clusterspec) | the cluster as committed: topology schema and validation, labels, addresses, machine-config patches, audit policy — no Pulumi resource |
| [layer/](layer) | the shim every layer shares: cluster resolution, provider, components, Helm |
| [clusterref/](clusterref) | the versioned output contract between the cluster tier and the layers |
| [charts/](charts) | one file per chart: the pin, the layer that installs it, the objects it must produce, the values whose misspelling fails silently, and the values template itself |
| [imagepolicy/](imagepolicy) | every image repository the charts run, and who signs it or why nothing does — the source of the admission policies |
| [values/](values) | the two functions that hand a rendered values file to a Helm release, which is the half that needs Pulumi's SDK |
| [workloads/](workloads) | what each chart is expected to produce, which the render check and the e2e suite both assert |
| [platform/](platform) | the names two layers must spell identically: storage class, ingress class, node ports |
| [cni/](cni) | which CNI a stack installs, and whether it agrees with what Talos did to kube-proxy |
| [clustersmoke/](clustersmoke) | can a cluster run a workload — the judgements behind `tools/smoke` |
| [pulumilog/](pulumilog) | the output vocabulary, the same one the taskfiles print |
| [report/](report) | the layout `platform:status`, `platform:drift` and `cluster:orphans` share: title, facts, table, sections, verdict, and the marks the taskfiles print |
| [stackstatus/](stackstatus) | the report behind `task platform:status`, without the Pulumi import that reads it |
| [stackdrift/](stackdrift) | the report behind `task platform:drift`, without the Pulumi import that reads it |
| [pulumilogin/](pulumilogin) | refuses to start against a backend this machine holds no credential for, before the CLI signs up a temporary account |
| [pulumiopts/](pulumiopts) | one function, because appending to a shared option slice writes into the caller's array |
| [hcloudtoken/](hcloudtoken) | the Hetzner token for a stack: an exported one first, then the encrypted stack config |
| [talossecrets/](talossecrets) | reads a cluster's Talos secrets bundle out of the Pulumi state that holds it |
| [secretout/](secretout) | the rule the credential-printing tools share: cluster credentials do not go to a terminal |

These are the IaC implementation; [ci/](../ci) beside them is the repository's
own gates, with nothing to run and nothing to import.
