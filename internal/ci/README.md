# ci

The repository's own gates: the cross-file contracts where the two halves sit
in different files or different languages, and a drift between them is silent
rather than an error.

Test files only — there is no command here, and nothing outside the module can
import it.

```bash
go test ./internal/ci/                                  # all of them
go test ./internal/ci/ -run TestLayers_ExportOnlyNamedOutputs -v  # one
```

They run as part of the unit suite, so `task -t Taskfile.dev.yaml go:test`, `task -t Taskfile.dev.yaml verify` and CI all
carry them.

The gates that read documentation or `.gitignore` are in [docs/](docs), a
package of their own: those paths are inert, so a prose-only pull request skips
the unit suite and runs `task -t Taskfile.dev.yaml docs:check` instead.
[citest/](citest) holds what both packages read the same way — the tracked
files, the taskfiles, the Pulumi projects — and is the one non-test package
here.

## What some of them guard

| Test file | Guards |
|-----------|--------|
| `bots_test.go` | one bot updates dependencies, so two do not open the same pull request |
| `clustertasks_test.go` | the two agreements between cluster tasks no error would report: a snapshot's sidecar name, and which node a talosctl call is aimed at |
| `committype_test.go` | the commit-type rule both the workflow and the commit hook run: what it answers, and the boundary it may not cross |
| `componentoutputs_test.go` | a component's registered outputs against the fields they are tagged as |
| `configkeys_test.go` | each layer's declared config against the config its code reads |
| `discards_test.go` | every discarded error says why it is discarded |
| `exits_test.go` | every main package reports a failure the same way, and none of them panics |
| `ingressclass_test.go` | the retired ingress-class literal stays out of executed code; `internal/pkg/platform.IngressClass` is the one spelling |
| `layers_test.go` | every layer with a component set calls `layertest.Check` |
| `location_test.go` | the probe location and every yaml fixture name a location that exists |
| `outputs_test.go` | every stack output a taskfile reads is declared in Go, and every export is named |
| `platformnames_test.go` | a shared name is declared once, in internal/pkg/platform, not twice |
| `release_test.go` | the release publishes only the commit CI passed, one at a time |
| `renovate_test.go` | Renovate against itself: its own regex matches every annotated pin and every pin is annotated, the paths it watches exist, and its schedule is not narrower than a best-effort cron can meet |
| `resourceoptions_test.go` | no `append` onto a shared option slice, which silently writes into the caller's array |
| `taskdocs_test.go` | every library task this repository refuses stays in its include's `excludes:` |
| `taskfilesplit_test.go` | the line between the two entry points: the root taskfile holds no check, and every workflow step passes `-t Taskfile.dev.yaml` |
| `taskguards_test.go` | the arguments a task takes and what it refuses without them: the stack, the layer, the usage it prints, the question before it changes anything |
| `tasklib_test.go` | every remote taskfile has the checksum Task will look for, ref included |
| `taskoutput_test.go` | what a task prints: the glyph vocabulary it shares with `internal/pkg/pulumilog`, and naming the layer when an operation finishes |
| `tasks_test.go` | — reading the taskfiles and cutting one into its tasks, which the task tests above share |
| `vendored_test.go` | each vendored manifest against its recorded digest; the half that asks upstream what it serves runs in the nightly workflow, which is the only place with a network |
| `workflows_test.go` | the workflows' own logic: relevance, caching, job wiring |

## Why not `.ci/`

The go tool ignores any path element beginning with `.` or `_`. In a `.ci/`
directory `go test ./...` skips every gate here and `./.ci/...` matches no
packages at all — so the suite would stay green while checking nothing.

## What earns a gate, and what does not

Each gate compares two halves that sit apart and whose drift reports nothing:
a storage class spelled two ways leaves every claim Pending, a renamed stack
output makes `jq` return `null`. All three have to hold, or the check costs
more than it returns:

1. **The two halves sit in different files or different languages**, so no
   compiler compares them.
2. **A drift is silent.** If it fails loudly — a build error, a red task, a
   line in `git status` — the gate is a second opinion nobody needs.
3. **It has already happened**, here, and is written down beside the check.

That leaves three things out on purpose. A gate over the SPELLING of the
implementation — a regex across Go source — fixes where the code is written
rather than what it does, and breaks on a refactor that changed nothing. A gate
over TASTE belongs in a review or a linter. A gate over NAVIGATION — a table describing these gates — protects a reader
from ten seconds of `ls`.
