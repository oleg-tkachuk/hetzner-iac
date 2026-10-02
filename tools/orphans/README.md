# orphans

Reports Hetzner resources that nothing in the cluster claims any more.
Read-only: it prints, exits 1 when it finds something, and never deletes.

```bash
go run ./tools/orphans <stack> <kubeconfig> <topology>
```

Needs the stack's Hetzner token (resolved as [token](../token) does) and
`kubectl` in PATH. It answers with or without a live cluster.

What it finds, all of it silent and all of it billed:

- volumes left by a destroyed layer — a StatefulSet's PersistentVolumeClaims
  outlive their Helm release, because `volumeClaimTemplates` survive an
  upgrade by design so it cannot eat the data;
- volumes left by a deleted cluster, whose API server is no longer there to
  tell the CSI driver to remove them;
- PersistentVolumes in phase **Released**, which Kubernetes will not bind to a
  new claim by itself. On the retaining class that is the intended outcome; on
  the default class the driver has not removed the volume yet, or cannot. The
  report says only that nothing will use it until somebody decides.

It never deletes: a volume whose PersistentVolume is gone still holds its data,
and this check cannot know whether that matters.

Run by `task cluster:orphans`, and last by `task destroy` — after everything
else is gone, every remaining resource really is an orphan.
