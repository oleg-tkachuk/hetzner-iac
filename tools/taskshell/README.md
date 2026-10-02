# taskshell

Runs shellcheck over the shell inside the taskfiles.

```bash
task -t Taskfile.dev.yaml shell
```

actionlint runs shellcheck over the workflows' `run:` blocks; nothing read the
taskfiles, which hold more shell than the workflows — including
`cluster:etcd:upload` and `cluster:etcd:restore`, the last shell anybody wants
to debug for the first time during an incident. Needs `shellcheck` in PATH.

## How it works

shellcheck cannot read a Taskfile: the shell is YAML scalars with Go template
actions in them, and `{{.ROOT}}` is not a word any shell parses. So each scalar
is extracted, the actions are resolved as far as the taskfile itself resolves
them, one file is written per block, and the findings are mapped back to the
line of the taskfile they came from.

- **Every key that holds shell** is read, not only `cmds`: `sh` in a
  precondition or a dynamic variable, and `status`.
- **A single-line variable is substituted for its value**, because the value is
  often what decides whether the shell is right.
- **A multi-line variable goes above the block instead**, so it does not shift
  every line below it. Its findings are reported at its own declaration rather
  than at each task that interpolates it.

Anything else — a Task special variable, a value passed on the command line, a
`range` — becomes `${TASK_TEMPLATE}`, a variable whose value shellcheck cannot
know. Not an empty string, which turns `"{{.ROOT}}"/bin` into `""/bin`, and not
a literal, which turns `[ -n "{{.stack}}" ]` into a comparison between two
constants.

## Suppressing a finding

With shellcheck's own directive, inside the block, with the reason:

```yaml
- cmd: |
    # shellcheck disable=SC2086 # CLI_ARGS is several arguments, so it splits on purpose
    go run "{{.ROOT}}/tools/golangci" run {{.CLI_ARGS | default "./..."}}
```

`# shellcheck disable=SC2086 -- reason` does not work. It is a parse error, and
the finding is not suppressed.

## Debugging a false positive

A false positive here is a template this resolved differently from the way Task
does, which is only visible in the extracted file:

```bash
TASKSHELL_KEEP=1 go run ./tools/taskshell
```

keeps the blocks on disk and prints the directory.
