# orphans

Reports Hetzner resources that nothing in the cluster claims any more.
Read-only: it prints, exits non-zero when it finds something, and never
deletes.

```bash
go run ./tools/orphans <stack> <kubeconfig> <topology>
```

The two ways this platform loses track of a resource are both silent and both
billed:

- a StatefulSet's PersistentVolumeClaims outlive their Helm release — that is
  Kubernetes behaving as designed, since `volumeClaimTemplates` survive an
  upgrade so it cannot eat the data, but it means destroying a layer leaves
  its volumes, and 160 GiB were found that way;
- deleting a cluster deletes the API server that would have told the CSI
  driver to remove a volume, so the volume stays with nothing anywhere
  referring to it.

It never deletes on purpose. A volume whose PersistentVolume is gone still
holds the data that was on it, and this check cannot know whether that
matters. It answers with or without a live cluster.

Run by `task cluster:orphans`, and last by `task destroy` — after everything
else is gone, every remaining resource really is an orphan.
