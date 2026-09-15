# stack

Answers and settles questions about Pulumi stacks.

```bash
go run ./tools/stack exists <project-dir> <stack>   # is it there
go run ./tools/stack ensure <project-dir> <stack>   # make sure it is; prints created|selected
go run ./tools/stack ref    <project-dir> <stack>   # the fully qualified <org>/<project>/<stack>
```

One shell idiom appeared three times — in the cluster tier's init, in the
platform layers' init, and in a preview run:

```bash
pulumi stack ls --json | jq -e --arg s "$STACK" 'any(.[]; .name == $s)'
```

Three copies of a pipeline is three places to get it wrong, and it was the
reason `jq` was a prerequisite at all.

`ensure` makes the whole decision rather than half of it. The obvious
`pulumi stack init || pulumi stack select` is worse than it looks: it sends
init's stderr to `/dev/null` to keep the "already exists" case quiet, so **any**
init failure — a rejected stack tag, a bad token, no network — surfaces only as
select complaining the stack does not exist. That cost a real diagnosis once:
init was refusing a description over 256 characters, and the operator saw
`no stack named 'dev' found`.

Run by `task cluster:init` and `task platform:init`.
