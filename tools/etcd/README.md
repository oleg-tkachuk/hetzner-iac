# etcd

Answers whether a file is an etcd snapshot, and what it holds.

```bash
go run ./tools/etcd verify --cluster <cluster name> <snapshot>
```

It exists because `task cluster:etcd:restore` wipes the EPHEMERAL partition of
every control-plane node before it restores anything, and doing that on a
truncated download turns a recoverable incident into an unrecoverable one. So
the file is checked before the cluster is touched.

The check is bbolt's own: opening the database validates the magic, format
version, page size and meta page checksum, and every other page is then walked
with bbolt's consistency check before anything is read. `etcdutl` is not used
because it brings the etcd server and raft with it.

It refuses a snapshot taken from another cluster. Every etcd member is named
after its control-plane node, and so after the cluster, so the snapshot itself
says where it came from; a file name can be copied or renamed.

It reports the **consistent index**, not the MVCC revision `talosctl etcd
snapshot` prints. They are different counters; comparing one against the other
mid-restore compares the wrong numbers.

Run by `task cluster:etcd:snapshot`, which reads back what it just wrote, by
`task cluster:etcd:restore` before it wipes anything, and by
`task cluster:etcd:upload` before it sends anything.
