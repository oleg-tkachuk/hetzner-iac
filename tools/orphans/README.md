# orphans

Reports Hetzner resources that no stack holds and nothing in the cluster uses.
Read-only: it prints, exits 1 when it finds something, and never deletes.

```bash
go run ./tools/orphans <stack> <kubeconfig> <topology> <project dir>…
```

Needs the stack's Hetzner token (resolved as [token](../token) does), a login
to the projects' Pulumi backend, and `kubectl` in PATH. It answers with or
without a live cluster.

Every kind the project lists is read: servers, volumes, load balancers, primary
and floating IPs, snapshots, networks, firewalls, placement groups, SSH keys,
certificates, DNS zones, storage boxes and their subaccounts. Server backups
and storage box snapshots are not: they go with the resource they belong to.

Two claimants account for a resource:

- **a stack** — every stack of every project directory is exported, and a
  resource whose ID any state holds is that stack's to destroy. Every stack,
  not only `<stack>`, so a project shared by two environments reports
  neither's. A DNS zone is claimed by the record sets a stack writes into it;
- **the cluster** — a volume by its PersistentVolume, a CCM load balancer by
  its Service, a server by its node, and a primary IP by whatever it is
  assigned to (the API makes a load balancer's addresses itself).

Talos snapshots are judged against the version the topology pins.

What it finds, all of it silent and most of it billed:

- volumes left by a destroyed layer — a StatefulSet's PersistentVolumeClaims
  outlive their Helm release, because `volumeClaimTemplates` survive an
  upgrade by design so it cannot eat the data;
- volumes left by a deleted cluster, whose API server is no longer there to
  tell the CSI driver to remove them;
- PersistentVolumes in phase **Released**, which Kubernetes will not bind to a
  new claim by itself. On the retaining class that is the intended outcome; on
  the default class the driver has not removed the volume yet, or cannot. The
  report says only that nothing will use it until somebody decides;
- anything this repository labelled that no state holds — a destroy that
  stopped part-way, or a stack removed with `--force`;
- anything made by hand or by another tool, which no state ever held.

A stack that exists and cannot be exported is an error, not an empty set, and
so is a backend with no stacks at all while the cluster's servers exist: either
would report every resource in the project.

It never deletes: a volume whose PersistentVolume is gone still holds its data,
and this check cannot know whether that matters.

Run by `task cluster:orphans`, and last by `task destroy` — after everything
else is gone, every remaining resource really is an orphan.
