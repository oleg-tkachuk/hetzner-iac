# etcd

Answers whether a file is an etcd snapshot, and what it holds.

```bash
go run ./tools/etcd verify <snapshot>
```

It exists for one moment: `task cluster:etcd:restore` wipes the EPHEMERAL
partition of every control-plane node before it can restore anything, and
doing that on the strength of a truncated download turns a recoverable
incident into an unrecoverable one. So the file is checked before the cluster
is touched.

The check is bbolt's own. A snapshot is a bbolt database, and opening one
validates the magic, the format version, the page size and the meta page
checksum — all of which this used to read by hand from copied offsets. Not
`etcdutl`, which has the same check as a library and brings the etcd server
and raft with it: thirteen modules to read a header.

It reports the **consistent index**, not the MVCC revision `talosctl etcd
snapshot` prints. Two different counters — measured on one snapshot as 6028
against 39170 — and reporting either as the other has somebody comparing the
wrong numbers mid-restore.

Run by `task cluster:etcd:snapshot`, which reads back what it just wrote, and
by `task cluster:etcd:restore` before it wipes anything.
