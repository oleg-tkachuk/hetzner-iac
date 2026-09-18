# docs

What each document covers, and when it is the one to open.

| Document | Open it when |
|----------|--------------|
| [design.md](design.md) | you want to know why it is shaped this way: what each layer owns, what the cluster tier publishes, and which decisions are load-bearing |
| [configuration.md](configuration.md) | you are filling in a topology or a stack's config, or moving state somewhere else |
| [domain.md](domain.md) | you want the cluster reachable from outside: the two fields, the zone, the certificate |
| [networking.md](networking.md) | you are asking how a packet reaches a pod, or what stops one that should not |
| [commands.md](commands.md) | you need the task that does a thing, with its arguments and what it refuses to do without them |
| [operations.md](operations.md) | a cluster exists and you are running it: stopping and starting, the checks worth running, reaching it with kubectl |
| [recovery.md](recovery.md) | a version moves or something is wrong: upgrades, etcd snapshots, uploading one, restoring from one |
| [ci.md](ci.md) | you are changing what lands, or working out why a check ran — or did not |
| [merged-layers.md](merged-layers.md) | you are looking at the experiment that puts two former layers in one Pulumi project: what keeps them apart, what keeps them independent, and what the state migration costs |
| [adr/](adr/) | you want the decisions this is built on, and the consequences accepted with them |

The [root README](../README.md) is the entry point: what this builds, the
quick start, and what has to be installed first.
