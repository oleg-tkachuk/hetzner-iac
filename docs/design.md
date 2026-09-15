# Why it is shaped this way

Four decisions explain most of the repository. Each one has a cost, and the
cost is named.

## What lives inside what

The seam this repository is built on: one project writes to Hetzner, and every
layer writes only to Kubernetes. Destroying the cluster tier takes everything
above it; destroying a layer takes only its own namespaces.

```mermaid
flowchart TB
    %% Palette from the README badges: Hetzner red, Talos orange, Kubernetes
    %% blue, so the header and the diagrams read as one thing and the colour
    %% says whose territory a box is in.
    %%
    %% Fills are pale and text is near-black explicitly. GitHub renders this in
    %% both themes, and a colour left to the theme picks one of them and is
    %% unreadable in the other.
    classDef hetzner fill:#fde8eb,stroke:#d50c2d,stroke-width:1px,color:#1f2328
    classDef talos fill:#fff0e0,stroke:#ff7300,stroke-width:1px,color:#1f2328
    classDef kube fill:#e7effc,stroke:#326ce5,stroke-width:1px,color:#1f2328
    classDef derived fill:#f6f8fa,stroke:#8c959f,stroke-width:1px,stroke-dasharray:4 3,color:#1f2328
    classDef state fill:#f6ecf7,stroke:#8a3391,stroke-width:2px,color:#1f2328

    subgraph hetzner["☁️ Hetzner Cloud project — infra/cluster, plus two layers that own one resource each"]
        direction TB
        net["private network<br/>+ subnet"]
        fw["firewall"]
        pg["placement group"]
        snap[("Talos snapshot<br/>task cluster:image:bake")]

        subgraph servers["servers"]
            direction LR
            cp["control plane"]
            wk["worker pools"]
        end

        apilb(["load balancer for the API<br/>the endpoint every certificate names"])
        inglb(["load balancer for ingress<br/>layers/40-ingress"])
        box[("Storage Box + subaccount<br/>layers/60-backup")]
    end

    subgraph talos["Talos on those servers"]
        direction LR
        etcd[("etcd")]
        api["kube-apiserver"]
    end

    subgraph k8s["Kubernetes — every layer writes only here"]
        direction TB
        ks["kube-system<br/>layers/10-node-platform"]
        pol["cluster-wide policy<br/>layers/20-network-policy"]
        cmns["cert-manager, external-secrets<br/>layers/30-cluster-services"]
        tns["traefik<br/>layers/40-ingress"]
        argons["argocd<br/>layers/50-gitops"]
    end

    trust[("Pulumi state<br/>the cluster CA lives only here")]

    trust -.->|"every certificate descends from it"| talos
    servers ==> talos
    talos ==> k8s
    inglb ==>|"tcp/80, tcp/443 to a pinned nodePort"| servers
    etcd -.->|"task cluster:etcd:upload — restic over sftp"| box

    class net,fw,pg,snap,cp,wk,box hetzner
    class apilb,inglb hetzner
    class etcd,api talos
    class ks,pol,cmns,tns,argons kube
    class trust state

    style hetzner fill:#fffafb,stroke:#d50c2d,stroke-width:2px,color:#1f2328
    style servers fill:#fde8eb,stroke:#d50c2d,stroke-dasharray:3 3,color:#1f2328
    style talos fill:#fffbf5,stroke:#ff7300,stroke-width:2px,color:#1f2328
    style k8s fill:#f7faff,stroke:#326ce5,stroke-width:2px,color:#1f2328
```

The ingress load balancer is the one resource that crosses the seam, and it
crosses from the wrong side on purpose: a layer asks Kubernetes for a Service,
and the cloud controller manager turns that into a Hetzner resource. No layer
holds a Hetzner credential to do it with.

The box outside all three is the one worth staring at. Every certificate in
the cluster descends from a secrets bundle that exists only in Pulumi's state:
`Protect` stops a destroy from taking it, which is not the same as a second
copy existing anywhere. It is also what makes an etcd snapshot restorable at
all, so `task cluster:secrets:export` writes it somewhere else — see
[operations.md](operations.md#the-two-halves-of-a-backup).

## Each layer is its own Pulumi project

A layer can be previewed, applied and destroyed on its own, and reads the
cluster's kubeconfig through a `StackReference` rather than sharing state with
it. Upgrading Cilium does not mean planning a change to Argo CD.

Independence has a boundary worth stating: layers are independently
*appliable*, not order-free. On an empty cluster nothing schedules before the
cloud controller manager clears Talos's `uninitialized` taint, and nothing
networks before the CNI. `task platform:apply layer=all` walks them in order; the
order lives once, in the Taskfile, and CI derives its matrix from the same
list.

## Every stack output is a named constant

A stack output is an interface, and half its consumers are not Go. The tier's
`kubeconfig` and `talosconfig` are read by `cluster:kubeconfig` and
`cluster:talosconfig`; the backup layer's five are read by `cluster:etcd:upload`
through jq. Those callers cannot import a constant, so they spell the name
again — and when the two spellings drift nothing errors: `pulumi stack output`
prints nothing, jq answers `null`, and the task reports the layer as unapplied,
which points the operator at an apply that will not fix it.

So every export in every project names a constant, whether or not a machine
reads it today. Uniform rather than "name the ones that matter", because who
reads an output changes: `ingressIp` is read by a person now and by whatever
writes DNS records later, and a rename at that point is a rename in two
languages. It also makes the published set greppable —
`grep Output internal/pkg/clusterref layers` is the whole list.

Two tests hold it, and they catch different halves.
`TestLayers_ExportOnlyNamedOutputs` refuses an export written as a literal.
`TestOutputs_ReadByShellAreDeclaredInGo` takes every name a taskfile reads and
requires a constant to publish it, which catches a rename on either side.
`internal/pkg/clusterref` additionally pins each constant's value, and
`TestOutputNames_ArePinnedWithoutException` counts the pins against the
constants, because that list had gone stale by three.

## What Pulumi owns, and what it deliberately does not

The `pulumi-hcloud` provider offers 32 resource types. This repository uses
eleven, and the gap is not all oversight — three quarters of it is a decision.

Owned here: the network and its subnet, the firewall, the placement group, the
servers, the API load balancer with its network attachment, service and
label-selector target, the ingress load balancer with the same four, its `A`
and `AAAA` records, and the Storage Box with its subaccount.

**Not owned, and not to be.** Each of these has one reason:

| Thing | Who owns it | Why not Pulumi |
|---|---|---|
| Volumes behind a `PersistentVolumeClaim` | the CSI driver | dynamic provisioning is the point; Pulumi owning them means abandoning claims. `cluster:orphans` covers the gap |
| A load balancer for a workload's `Service` | the CCM | the workload's Service owns it. The *ingress* one moved because the platform owns that one |
| Objects inside a Helm release | Helm | Pulumi owns the Release. Owning both puts two reconcilers on one object |
| The Talos snapshot | `task cluster:image:bake` | Hetzner has no image-upload API. `hcloud.Snapshot` takes a `ServerId`, so Pulumi could take the snapshot but not write the disk — the imperative half stays either way |
| A Talos or Kubernetes upgrade | `talosctl` | a procedure with an order, not a desired state |
| Workloads | Argo CD | that is what the GitOps layer is for |

**DNS records are `40-ingress`'s**, because that layer is where the load
balancer's address is known. The zone is LOOKED UP, never created: a zone is
delegated once, by pointing a registrar's `NS` records at Hetzner's
nameservers, and that delegation outlives any cluster here — a zone this stack
owned would be a zone `pulumi destroy` deletes, with every record in it. Both
an `A` and an `AAAA`, because a Hetzner load balancer has both and an
IPv4-only record fails for exactly the clients nobody tests from.

The domain may be registered anywhere. What has to be at Hetzner is the
authoritative DNS, and `metadata.dnsZone` may be left empty when it is not —
the layer then says the records are somebody else's to write rather than
staying silent.

One thing could still move and has not, and it is measured:

- **Primary IPs.** Every public address today is implicit and carries
  `auto_delete: true`, so a server replacement takes its address with it — and
  servers here are `DeleteBeforeReplace`, so that happens on any replacing
  change, not only on a teardown. An explicit `hcloud.PrimaryIp` survives it,
  at the same cost while attached, and keeps billing while it is not. It does
  **not** help the address DNS would point at: that is a load balancer's, and
  an LB IPv4 is neither a primary nor a floating IP — `floatingIpAssignment`
  takes a `ServerId` and nothing else.

One API note worth keeping, because it costs an afternoon otherwise: Hetzner
serves these from two bases. Storage Boxes are on the unified API —
`api.hetzner.com/v1/storage_boxes` — and answer `api route not found` on
`api.hetzner.cloud/v1`. Zones are the other way round. The same project token
reaches both.

## The cluster is a committed file

`infra/cluster/cluster.<stack>.yaml` describes the topology, so a cluster is
reviewable in a diff before it exists and reproducible from a clone. The
template for it is
[cluster.example.yaml](../infra/cluster/cluster.example.yaml). It is
sparse — anything omitted keeps the default in `internal/pkg/hetzner` — and it is
validated against the same code the Pulumi program runs, so the check cannot
drift from the thing it checks.

One field keeps that file out of git: `network.adminCIDRs` is the operator's
own address. `cluster.example.yaml` is committed with an RFC 5737 placeholder
instead.

The consequence is that CI cannot preview the cluster tier — a runner has no
topology. Cluster-tier drift is checked by hand with `task cluster:plan`.

## The cluster tier stops at "a Kubernetes API that answers"

It installs no CNI: Talos would otherwise install Flannel, which would then
have to be removed before Cilium could take over. Nodes are `NotReady` until
`layers/10-node-platform` runs. That is the handover point, not a failure — and
it is why that layer also owns the cloud controller manager, which cannot be
scheduled onto a node no CNI has made Ready.

```mermaid
sequenceDiagram
    autonumber
    actor operator
    participant tier as infra/cluster
    participant hcloud as Hetzner Cloud
    participant k8s as Kubernetes API
    participant layers as layers/*

    rect rgb(253, 232, 235)
        operator->>tier: task cluster:apply
        tier->>hcloud: private network, firewall, servers
        tier->>hcloud: Talos machine configuration, then bootstrap
        hcloud-->>k8s: the API answers
        tier-->>operator: kubeconfig and talosconfig, as stack outputs
    end

    Note over tier,k8s: the cluster tier installs no CNI,<br/>so every node stays NotReady until the next phase

    rect rgb(231, 239, 252)
        operator->>layers: task platform:apply layer=all
        layers->>k8s: layers/10-node-platform installs the CNI — Cilium
        Note over layers,k8s: nodes become Ready
        layers->>k8s: the remaining layers, in dependency order
    end
```

## The default deny is opt-in

`layers/20-network-policy` sits immediately after the CNI because Cilium is
what enforces its resources — the CRDs do not exist until the chart is
installed. What it carries is a `CiliumClusterwideNetworkPolicy` per flow the
cluster cannot lose (host to pod, pod to DNS, pod to the API server through
KubePrism, scraping, the few pod-to-pod paths the platform actually uses) and
one default deny, separately.

`network-policy:enabled` is `false` by default, and that is not timidity. In
Cilium, *any* policy that selects an endpoint puts that endpoint into
default-deny for the direction the policy mentions — so there is no such thing
as an allow rule that changes nothing, and a missing rule is a silent
connection timeout rather than a rejected apply. The allow policies therefore
set `enableDefaultDeny: {ingress: false, egress: false}`, which makes them
genuinely additive, and the deny is the one resource the flag gates.

With the allow policies applied and the deny still off, Cilium reports
something that reads like the opposite:

    $ cilium-dbg endpoint list
    ENDPOINT   POLICY (ingress)   POLICY (egress)
               ENFORCEMENT        ENFORCEMENT
    30         Enabled            Enabled

That is not the deny. `ENFORCEMENT` says the datapath now consults the
endpoint's policy map, which it does as soon as any policy selects the
endpoint; what the map contains is the question, and it contains a wildcard:

    $ cilium-dbg bpf policy get 30
    Allow    Ingress   ANY             ANY   24340727 bytes
    Allow    Egress    ANY             ANY     569433 bytes
    Allow    Ingress   reserved:host   ANY      62587 bytes

The first two lines are `enableDefaultDeny: false` doing its job. A default
deny is precisely the absence of those wildcards, so their presence — not the
word Enabled — is what says nothing is being dropped. `hubble observe
--verdict DROPPED` answers the same question from the other end, and needs no
interpreting.

Turn it on with the flows in front of you: `task cluster:hubble` prints what
the cluster is doing now.

## Three control planes, and etcd on the private network

The shipped shape is three control planes behind a load balancer, which is the
smallest number that means anything: validation refuses an even count, because
a second member tolerates no more failures than one while costing twice as
much. The three land in a spread placement group, so they are three failure
domains rather than three processes on one machine.

The load balancer is not a convenience. It is the endpoint signed into every
certificate, which is what makes a member replaceable — with a node's own
address there, replacing that node reissues everything that named it.

One thing HA needs that a single node never did: **etcd has to advertise
inside the private network.** Left alone it advertises whichever address the
node has first, and on Hetzner that is the public one — where the perimeter
firewall opens tcp/6443 and tcp/50000 and nothing else, so the members cannot
reach each other's tcp/2380. Measured on the first three-member cluster built
here: two members, one of them a learner for ever, and the third never
joining. `internal/pkg/hetzner.BuildEtcdPatch` pins it, in a document applied to
control planes only — Talos refuses the section on a worker, which
`task cluster:machine-config:check` says out loud.

Verified by turning a member off: the API kept answering through the load
balancer and Kubernetes kept accepting writes on the remaining two.

## How a request reaches a pod

Two halves of one setting, and enabling either alone fails every request
through the load balancer. The load balancer is told to send a PROXY header;
Traefik accepts one only from addresses it is told to trust, and its default is
to trust nobody.

The load balancer is created by `layers/40-ingress` through the Hetzner
provider, not by the cloud controller manager. A Service of type LoadBalancer
would hand the job to the CCM, and that was measured to cost two things: the load balancer was invisible to `plan` and `destroy` and showed
up only in the bill, and it had no targets at all — the CCM will not target a
node carrying `node.kubernetes.io/exclude-from-external-load-balancers`, which
Talos puts on every control-plane node. So the Service is a `NodePort` on
pinned ports and the load balancer selects its targets by cluster label.

```mermaid
flowchart LR
    %% Same palette as the diagram above, and the same reason for spelling the
    %% colours out rather than inheriting the theme's.
    classDef outside fill:#f6f8fa,stroke:#8c959f,stroke-width:1px,color:#1f2328
    classDef edge fill:#fde8eb,stroke:#d50c2d,stroke-width:2px,color:#1f2328
    classDef inside fill:#e7effc,stroke:#326ce5,stroke-width:1px,color:#1f2328
    classDef gate fill:#fff0e0,stroke:#ff7300,stroke-width:2px,color:#1f2328

    client(["client"])
    lb["Hetzner load balancer<br/>public IPv4 and IPv6<br/>created by Pulumi, targets by cluster label"]

    subgraph private["🔒 private network — network.nodeSubnet"]
        direction LR
        node["node<br/>private address only<br/>nodePort 30080 / 30443"]
        traefik["Traefik<br/>entry points: web, websecure<br/>trusts the PROXY header from network.nodeSubnet"]
        svc["Service"]
        pod(["pod"])
    end

    client -->|"tcp/80, tcp/443"| lb
    lb ==>|"PROXY header<br/>private target, pinned nodePort"| node
    node --> traefik
    traefik --> svc
    svc --> pod

    class client outside
    class lb edge
    class traefik gate
    class node,svc,pod inside

    style private fill:#f7faff,stroke:#326ce5,stroke-width:2px,color:#1f2328

    %% The one edge worth pointing at: it is the hop that carries the PROXY
    %% header. Indexed by edge order, so adding an edge above this one moves it.
    linkStyle 1 stroke:#d50c2d,stroke-width:3px
```

The trusted range is the node subnet the cluster tier publishes, not a wider
one: the load balancer reaches the nodes privately, and a wider range would
accept a spoofed header from any pod. Nothing trusts `X-Forwarded-*` in
addition — the client address arrives in the PROXY header, and trusting both
would accept a forged one.

## What the cluster encrypts, and what it does not

Kubernetes Secrets are encrypted at rest without this repository doing
anything: the Talos secrets bundle the Pulumi provider generates carries a
secretbox key, and Talos wires it into the API server's
`--encryption-provider-config`. That covers the `secrets` resource and nothing
else.

The volumes themselves are encrypted by two `VolumeConfig` documents in the
cluster patch — STATE, which holds the machine config and the node's
certificates, and EPHEMERAL, which holds `/var` and so etcd's data directory.
Without them a restored snapshot or a volume attached to another machine is
readable, which is a far cheaper attack than reaching the running node.

The key provider is `nodeID`, derived from the node's UUID. It is deliberately
not protection against someone who can already run commands on the node —
Talos offers `tpm` for that, which needs SecureBoot and a TPM a Hetzner Cloud
instance does not have, and `kms`, which needs a key server this repository
does not run. `static` would put the passphrase in the machine config beside
the data it protects.

Talos has no in-place encryption: enabling this on a node that already exists
means wiping STATE, which is where its configuration lives. On a cluster that
is already running, the path is `task cluster:destroy` and a fresh
`cluster:apply`, then the layers.

## Every chart version is pinned in one place

`internal/pkg/charts` is the registry; floating tags are rejected by validation rather
than by convention. `task charts:outdated` compares each pin against its
upstream repository, and Renovate opens one pull request per chart — see
[ci.md](ci.md#chart-upgrades-arrive-as-pull-requests).

## Versions

Both the Talos and the Kubernetes version are pinned in the topology, and
neither derives from the other. An empty `kubernetes.version` takes
`DefaultKubernetesVersion` — also pinned — rather than whatever the configured
Talos release happens to ship.

That is not caution for its own sake. Deriving one from the other made a Talos
patch bump able to move Kubernetes a whole minor with no diff and no decision,
and it did: the first bring-up landed on v1.36.0, new enough that
`kube-apiserver` had removed a flag the machine config was passing, and the
control plane never started.

Upgrading Talos means bumping `talos.version` in the topology, re-running
`task cluster:image:bake`, then `task cluster:upgrade:talos`. Nodes are
upgraded in place and never replaced, which is why the server resource ignores
changes to its image.

`talos.architecture` is the other half of the same pin, and it is a replace
rather than an upgrade: `x86` and `arm` are different images on different
server types, so switching means a new snapshot and new nodes. The topology is
the single place that says which — the factory URL, the server types the
validator will accept, and the defaults those types fall back to all read that
one field. See
[configuration.md](configuration.md#cpu-architecture) for the costs and for
why Arm availability has to be checked per project rather than assumed.

## How pod traffic crosses a node boundary

This is the one piece of the design that a single-node cluster cannot test, and
it was wrong for as long as there was only one node to hide it.

A Hetzner private network is **routed, not switched**. Each server's private NIC
carries a `/32`, and the only on-link peer is the gateway:

```
eth1  inet 10.0.1.3/32
10.0.0.0/16 via 10.0.0.1 dev eth1
10.0.0.1 dev eth1 scope link
```

So a node has no way to reach another node's pod CIDR on its own. Two things can
supply one, and `network.routingMode` picks between them.

### native — the default

The hcloud CCM's route controller programmes one route per node inside the
Hetzner network:

```
10.244.0.0/24 -> 10.0.1.4
10.244.1.0/24 -> 10.0.1.2
10.244.2.0/24 -> 10.0.1.3
```

Those live in Hetzner's router, not in the node. For a pod packet to reach them
the node must send it to the gateway, so the cluster tier writes exactly that
into every machine config:

```yaml
machine:
  network:
    interfaces:
      - interface: eth1
        dhcp: true          # keeps the private address Hetzner hands out
        routes:
          - network: <podCIDR>
            gateway: <first address of ipRange>
```

The gateway is derived from `network.ipRange` rather than written as
`10.0.0.1`, which is correct only while the range keeps its default.

### tunnel — VXLAN between node addresses

`routingMode: tunnel` wraps pod packets in VXLAN, addressed node to node. It
needs nothing from the private network's routing and nothing from the CCM's
route controller — only that nodes can reach each other, which is the property
that stayed true throughout the failure below.

That makes it the right choice in two situations: when the private network's
routing is itself under suspicion, and on any provider whose network does not
route pod CIDRs at all. It also runs unfiltered here, because Hetzner Cloud
firewalls apply to the public interface only.

What it costs: about 50 bytes of header a packet, the MTU reduction that comes
with them, and encapsulated captures.

**Switching is a maintenance operation, not a toggle.** Every Cilium agent
restarts and pod traffic breaks while they do. It is one Helm value and no Talos
apply, because the gateway route is installed in *both* modes — under tunnel it
is simply never used, Cilium's own per-node routes being more specific.

### autoDirectNodeRoutes cannot work here, in either mode

It asks Cilium to install a route to a peer's pod CIDR *via that peer's
address*. On a `/32` with only the gateway on-link there is no such path, and
Cilium says so rather than guessing:

```
Unable to install direct node route
  route="{Dst: 10.244.0.0/24  Gw: 10.0.1.4}"
  error="route to destination 10.0.1.4 contains gateway 10.0.0.1,
         must be directly reachable"
Failed to apply node handler during background sync.
```

It was set to `true`, and the result was that pod-to-pod traffic across nodes
had **no route at all**. What that looked like, in order: CoreDNS on two nodes
unreachable from the third, so roughly a third of DNS queries timed out; the
hcloud CSI controller — scheduled on the node without a CoreDNS replica — unable
to resolve `api.hetzner.cloud`, hanging before it opened its gRPC socket; its
liveness probe therefore refused; kubelet killing it every twenty seconds
(`initialDelaySeconds: 10` plus `periodSeconds: 2` × `failureThreshold: 5`);
and its three sidecars exiting behind it with "Lost connection to CSI driver".
706 restarts, and the only visible symptom was that no volume could be
provisioned.

The flag the error message suggests, `direct-routing-skip-unreachable`, is not
a fix. It stops Cilium retrying a route that cannot work and leaves the traffic
with nowhere to go — quieter logs, same broken cluster.

The cheapest check that would have caught all of it is `cilium-health status`,
which reported `1/3 reachable` with node-level reachability at `1/1` and
endpoint-level at `0/1` for both peers — host paths fine, pod paths dead.

## The Hetzner token travels with the kubeconfig

The cluster tier exports the token and every layer reads it through the same
stack reference that carries the kubeconfig, so it is typed once.

The objection to exporting a credential — that it lands in the state of every
referencing stack — is already true of the kubeconfig and the talosconfig on
that channel, both strictly more powerful than an API token.

## Asking the real tool

Three checks run offline against the software itself rather than against this
repository's own tests, because a test agreeing with the code that produced it
proves nothing about what Helm or Talos will accept:

| Check | Asks |
|-------|------|
| `task charts:validate` | the upstream repositories, that every pin is an exact version that resolves |
| `task charts:render-check` | `helm template`, then the pinned Kubernetes version's own schema |
| `task cluster:machine-config:check` | `talosctl`, that the machine configuration is one it would apply |

`task verify` runs all three. They need `helm`, a `talosctl` matching the
pinned Talos minor, and a running Docker.

What they have caught, none of which would have failed a `pulumi up`:

- `kubeProxyReplacment` — one letter short. Helm accepts an unknown key
  silently, even for a chart shipping a `values.schema.json`, so the default
  stayed and the rendered output read `kube-proxy-replacement: "false"`. Talos
  runs with kube-proxy disabled, so that is a cluster where every ClusterIP
  blackholes, started successfully.
- A DaemonSet needing host access in a namespace Talos does not exempt from
  Pod Security Admission — its pods are never created, and Helm waits out its
  whole timeout with nothing to show.
- `machine.network.hostname`, which Talos rejects outright.
- A Talos version pinned ahead of what the provider's generator knows.

The first is why [internal/pkg/chartsettings](../internal/pkg/chartsettings) exists: the handful
of values whose misspelling fails silently are constants there, and
`render-check` asserts the **effect** each one has on the rendered chart rather
than that the key was set.

## What a run prints

One vocabulary, printed from two places: [internal/pkg/pulumilog](../internal/pkg/pulumilog) in
the layers and the taskfiles' own lines, both borrowed from the
[taskfiles](https://github.com/oleg-tkachuk/taskfiles) library so that `task`
and `pulumi up` read as one tool.

| glyph | means | survives a pulumi run |
|-------|-------|-----------------------|
| `◉` | work starting | no |
| `✔` | work finished | no |
| `○` | deliberately not done | **yes** |
| `▲` | configured, and will not do what it looks like | **yes** |
| `✖` | failed — tasks only | — |

The line is `<glyph> <scope> · <area> · <what happened>`, with `→` for "became"
or "went to". `✖` has no Go counterpart on purpose: a layer reports failure by
returning an error, which Pulumi formats itself.

The last two glyphs are the point. A layer that installs cert-manager and no
ClusterIssuer is the most confusing thing this repository can do, so those
lines go to Pulumi's permanent diagnostics and are still on screen when the run
ends. Progress lines are ephemeral, or the summary is one line per release and
nobody reads it.

`TestTaskGlyphs_MatchThePulumiLogger` holds the two halves equal. The Go side
had said its glyphs match the taskfiles "exactly" since it was written, and
nothing checked it.

`NO_COLOR` drops the escape codes and keeps the glyphs. A TTY check would be
wrong rather than merely unhelpful: a Pulumi program's output is captured by
the CLI over gRPC, so stdout is never a terminal.
