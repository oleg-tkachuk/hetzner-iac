# taskshell

Runs shellcheck over the shell inside the taskfiles.

```bash
task -t Taskfile.dev.yaml shell
```

## Why it exists

`.github/workflows/security.yaml` explains why actionlint was added: this
repository "carries a few hundred lines of shell inside YAML that no Go test
and no linter was reading". actionlint closed that for `.github/workflows/`,
where it runs shellcheck over every `run:` block.

It did not close the taskfiles, which hold more shell than the workflows do.
The two longest blocks in them are `cluster:etcd:upload` and
`cluster:etcd:restore` — the procedures that put a cluster back after it is
gone, and the last shell anybody wants to debug for the first time during an
incident.

The first run found 24 findings across four taskfiles. Two of them were an
existing `# shellcheck disable=SC2086 -- splitting is intended`: shellcheck
takes a trailing comment after `#` and not after `--`, so the directive was a
parse error, the code it named was never disabled, and the rest of that block
went unanalysed. One was `export HCLOUD_TOKEN="$(go run tools/token …)"` in a
variable six tasks interpolate — `export` returns its own exit status, so
`set -e` never saw the token command fail and those tasks carried on with an
empty token.

## How it works

shellcheck cannot read a Taskfile: the shell is YAML scalars with Go template
actions in them, and `{{.ROOT}}` is not a word any shell parses. So each scalar
is extracted, the actions are resolved as far as the taskfile itself resolves
them, one file is written per block, and the findings are mapped back to the
line of the taskfile they came from.

Three details carry most of the correctness:

- **Every key that holds shell**, not only `cmds`: `sh` is a precondition and a
  dynamic variable, `status` decides whether a task runs at all, and shell that
  decides is shell that can be wrong.
- **A single-line variable is substituted for its value**, because the value is
  often what decides whether the shell is right. `_PL_ROW` is a printf format
  string; with the action left in place shellcheck reports that printf's
  arguments are ignored — true of the substitution, false of the taskfile.
- **A multi-line variable goes above the block instead**, so it does not shift
  every line below it. `_CL_CONTROL_PLANE` assigns `nodes`, and without its
  text above them every loop over `$nodes` reads an unassigned variable. Its
  findings are reported at its own declaration rather than at each of the tasks
  that interpolate it.

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
