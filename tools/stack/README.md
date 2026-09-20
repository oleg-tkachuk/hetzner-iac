# stack

Lists the cluster tier's environments.

```bash
go run ./tools/stack list <cluster-dir>
```

It reads two independent places: the backend knows which stacks exist, and the
working copy holds the topologies that describe them. Both halves are optional,
and the rows missing one are the interesting ones — `cluster.<stack>.yaml` is
gitignored, so a stack described in somebody else's clone shows as `missing`
rather than as a failure three tasks later, and a topology with no stack yet
shows as `no stack`.

Run by `task cluster:stacks`.

`exists`, `ensure` and `ref` used to live here and are now
[pulumi-kit](https://github.com/oleg-tkachuk/pulumi-kit)'s `cmd/stack`, which
the taskfiles call directly: none of them read anything of this repository's,
and two other projects wanted them.
