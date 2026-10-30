# nodes

Prints the public IPv4 address of every control-plane server of a cluster,
one per line.

```bash
go run ./tools/nodes dev platform-dev
```

From the Hetzner API through `hcloud-go`, not from the cluster: `cluster:etcd:restore`
runs when etcd is broken, and `talosctl get members` needs etcd to answer. It
refuses an empty match and a server without a public IPv4 rather than printing
nothing or a placeholder for the restore to loop over.

The token is resolved the way [token](../token) resolves it.
