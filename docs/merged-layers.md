# Two layers in one project

An experiment, on the branch `experiment/merge-layers-30-40`. It merges what
were `layers/30-cluster-services` and `40-ingress` into one Pulumi project and
keeps them distinguishable and independent inside it. Nothing here is merged on
`main`.

What it is trying to answer: the six platform layers cost seven stacks per
environment, a CI job whose only purpose is to stop a list of layer names
drifting from the directories, 86 uses of a `layer=` parameter across the task
surface, and an ordering that `internal/pkg/layer` itself describes as "the
operator's responsibility, not Pulumi's". Merging two of them is the cheapest
way to find out whether that trade was worth making six times.

## What keeps the two halves apart

Each former layer is a `layer.Group` — a `pulumi.ComponentResource` whose type
lands in every child's URN:

```
urn:pulumi:dev::cluster-services::hetzner-iac:platform:ClusterServices$kubernetes:helm.sh/v3:Release::keda
urn:pulumi:dev::cluster-services::hetzner-iac:platform:Ingress$kubernetes:helm.sh/v3:Release::traefik
```

Read off the real preview, not composed by hand. The consequence is that either
half can still be addressed on its own, which is the thing separate stacks gave
for free:

```bash
pulumi up      --target '**:Ingress$**'
pulumi destroy --target '**:Ingress$**' --target-dependents
```

The **type** is what carries this, not the name: a type is what appears in a
child's URN, so two groups sharing one would be one group as far as the state
and `--target` are concerned. `TestComponents_TheGroupsHaveDistinctTypes` holds
that, and `TestDeploy_TwoGroupsAreToldApartByTheirURNs` holds it one level down.

## What keeps them independent

Grouping is not ordering. A parent decides the URN and the display tree; it does
not make one child wait for another and it does not propagate a change from the
parent to its children.

What *would* couple them is an `After` across the boundary, because that becomes
a `DependsOn` — and then `destroy --target` on one group refuses without
`--target-dependents` and takes the other group's resource with it when given
one. So `order()` refuses one, with the pair named in the error, and
`TestComponents_NothingCrossesTheGroupBoundary` says it at test time rather than
at apply time.

There is one dependency a reader expects to find and will not: Traefik does not
follow cert-manager. cert-manager issues a certificate for an Ingress by
watching it at run time, so Traefik installs against a cluster with no issuer
and picks one up when there is one. An `After` there would read as documentation
and cost independence.

## Aliases, and what they are worth

Adding a parent changes a resource's URN. Without saying otherwise, Pulumi reads
that as the old resource gone and a new one arrived. Measured against the live
`dev` stack, both numbers from `task platform:plan stack=dev layer=30-cluster-services`:

| | to create | to delete | unchanged |
|---|---|---|---|
| with `Aliases{NoParent: true}` | 10 | 0 | **15** |
| without it | 23 | 13 | 2 |

Thirteen deletes and twenty-three creates, for a change that was supposed to
move nothing. For Helm releases that is an uninstall and a reinstall of
everything the cluster runs.

Of the 10 creates in the top row, two are the group nodes themselves — state
only, nothing in the cloud — one is the Hetzner provider this project did not
have, one is KEDA, which is pending its first apply either way, and the
remaining six are the ingress resources, which are still in the other stack.
See below.

## The part that is not a code change

A URN contains the **project** name. Merging two projects therefore rewrites
every URN in one of them, and no alias can fix that — an alias describes a
resource's history inside its own stack.

That is why the preview above still shows the ingress resources as creates: the
code is merged, the state is not. Moving it is `pulumi state move`.

### Rehearsed, on copies, on a local backend

Not reasoned about — run. Both live stacks were exported, imported into two
stacks on a `file://` backend under the same project and stack names so the URNs
stayed consistent, and the move was performed there. The live stacks were never
touched: `ingress/dev` still holds its 11 resources and `cluster-services/dev`
its 17.

Six URNs are enough. The providers are **not** listed:

```bash
pulumi state move --source <ingress stack> --dest <cluster-services stack> \
  'urn:pulumi:dev::ingress::hcloud:index/loadBalancer:LoadBalancer::ingress' \
  'urn:pulumi:dev::ingress::hcloud:index/loadBalancerNetwork:LoadBalancerNetwork::ingress-network' \
  'urn:pulumi:dev::ingress::hcloud:index/loadBalancerService:LoadBalancerService::ingress-https' \
  'urn:pulumi:dev::ingress::hcloud:index/loadBalancerService:LoadBalancerService::ingress-http' \
  'urn:pulumi:dev::ingress::hcloud:index/loadBalancerTarget:LoadBalancerTarget::ingress-targets' \
  'urn:pulumi:dev::ingress::kubernetes:helm.sh/v3:Release::traefik'
```

### Three things the rehearsal answered that reading could not

**The providers come along by themselves.** `state move` reported both of them
in its dependency list without being asked, and moved what was needed.

**The provider collision is not a problem on this CLI.** Both stacks hold a
`pulumi:providers:kubernetes::k8s` with the SAME name and DIFFERENT ids —
`ce7b0165…` in ingress, `11f498a9…` in cluster-services — which is exactly
[pulumi#16983](https://github.com/pulumi/pulumi/issues/16983), "provider already
exists in destination stack". It is fixed. The destination ended with ONE k8s
provider, its own, and traefik's reference rewritten onto it:

```
urn:…::cluster-services::kubernetes:helm.sh/v3:Release::traefik
  provider=urn:…::cluster-services::pulumi:providers:kubernetes::k8s::11f498a9-…
```

The hcloud provider, which the destination did not have, was moved in with its
own id — and its URN is already the one the merged program declares, so the
program adopts it instead of creating the `pulumi:providers:hcloud` the preview
showed.

**Everything lands root-parented**, under the destination's stack node. That is
precisely what `Aliases{NoParent: true}` describes, which is why the moved
resources are adopted under the group rather than replaced — the same mechanism
already measured as 15 unchanged on the live stack.

### Afterwards

The source keeps five resources — its stack node, the default provider, its
StackReference and the two providers — so it is finished with:

```bash
pulumi stack rm <ingress stack>
```

`state move` also warns that every moved resource depends on the SOURCE stack's
StackReference, and that the destination program must provide the equivalent
inputs. It does: the merged project has its own StackReference to the same
cluster stack. Worth reading the warning rather than skipping it, because for a
different pair of layers it would be a real gap.

### Order of operations

**Move first, then apply.** An apply before the move creates a second Traefik
release and a second load balancer, and the second load balancer is billable.

The config key moves with it. `ingress:loadBalancerType` becomes
`cluster-services:loadBalancerType`, because a Pulumi config key is namespaced
by the project — a stack that had the old one set has to have the new one set
again. That is in [configuration.md](configuration.md).

## What to look at before deciding

- Does `--target '**:Ingress$**'` feel like a replacement for `task
  platform:apply layer=40-ingress`, or like a thing you have to look up?
- Is the preview of the merged project fast enough to run as often as six
  smaller ones?
- Does the ingress group ever want applying without the services group in
  practice, or was the separation theoretical?

If the answers point the same way, the same move applies to the rest. If not,
one layer pair is a cheap thing to revert.
