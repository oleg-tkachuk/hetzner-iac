# ci

The repository's own gates: the cross-file contracts where the two halves sit
in different files or different languages, and a drift between them is silent
rather than an error.

Test files only — there is no command here, and nothing outside the module can
import it.

```bash
go test ./internal/ci/                                  # all of them
go test ./internal/ci/ -run TestGateMarkers -v           # one
```

They run as part of the unit suite, so `task go:test`, `task ci:verify` and CI all
carry them.

## What each guards

| Test file | Guards |
|-----------|--------|
| `adr_test.go` | each record's status and heading against the row the index gives it, both ways |
| `bots_test.go` | one bot updates dependencies, so two do not open the same pull request |
| `clustertasks_test.go` | the two agreements between cluster tasks no error would report: a snapshot's sidecar name, and which node a talosctl call is aimed at |
| `committype_test.go` | the commit-type rule both the workflow and the commit hook run: what it answers, and the boundary it may not cross |
| `componentoutputs_test.go` | a component's registered outputs against the fields they are tagged as |
| `configkeys_test.go` | each layer's declared config against the config its code reads, and both documentation tables |
| `dates_test.go` | no dates in prose — git already records when somebody learned something |
| `diagrams_test.go` | every layer a diagram names exists, and every diagram reference resolves |
| `discards_test.go` | every discarded error says why it is discarded |
| `exits_test.go` | every main package reports a failure the same way, and none of them panics |
| `externaltools_test.go` | every binary this repository runs is named in the prerequisites, the checks' list or the Brewfile |
| `gatemarkers_test.go` | the † in the command reference against the tasks the workflows run |
| `gitignore_test.go` | every main package's binary name has a `.gitignore` entry |
| `identifiers_test.go` | no tracked file names a real address, domain or ACME contact of this project's own; examples stay in the ranges RFC 5737 and RFC 2606 reserve |
| `ingressclass_test.go` | the retired ingress-class literal stays out of executed code; `internal/pkg/platform.IngressClass` is the one spelling |
| `layers_test.go` | every layer with a component set calls `layertest.Check` |
| `location_test.go` | the probe location and every yaml fixture name a location that exists |
| `outputs_test.go` | every stack output a taskfile reads is declared in Go, and every export is named |
| `platformnames_test.go` | a shared name is declared once, in internal/pkg/platform, not twice |
| `readmes_test.go` | every tool and every internal package documents itself; no README names a file that is not in the tree; the docs index lists every document |
| `renovate_test.go` | Renovate against itself: its own regex matches every annotated pin and every pin is annotated, the paths it watches exist, and its schedule is not narrower than a best-effort cron can meet |
| `renovatealerts_test.go` | vulnerabilityAlerts in renovate.json against the token permission the workflow tells you to grant, in whichever direction it is set |
| `resourceoptions_test.go` | no `append` onto a shared option slice, which silently writes into the caller's array |
| `taskdocs_test.go` | every task a document tells somebody to run exists, and none is one the library include leaves out |
| `taskguards_test.go` | the arguments a task takes and what it refuses without them: the stack, the layer, the usage it prints, the question before it changes anything |
| `tasklib_test.go` | every remote taskfile has the checksum Task will look for, ref included |
| `taskoutput_test.go` | what a task prints: the glyph vocabulary it shares with `internal/pkg/pulumilog`, and naming the layer when an operation finishes |
| `tasks_test.go` | — reading the taskfiles and cutting one into its tasks, which the four above share |
| `vendored_test.go` | each vendored manifest against its recorded digest; the half that asks upstream what it serves runs in the nightly workflow, which is the only place with a network |
| `workflows_test.go` | the workflows' own logic: relevance, caching, job wiring |

## Why not `.ci/`

The go tool ignores any path element beginning with `.` or `_`. In a `.ci/`
directory `go test ./...` skips every gate here and `./.ci/...` matches no
packages at all — so the suite would stay green while checking nothing.
Measured before choosing, with a deliberately failing test in each place.

## Why these exist at all

Each one is a failure that already happened here and reported nothing. A
storage class spelled two ways leaves every claim Pending with nothing saying
why. A renamed stack output makes `jq` return `null`, which the caller reads as
an empty password. A Renovate regex that stops matching opens no pull request,
for ever, silently. Two halves and nothing comparing them is the shape; these
are the comparisons.

## What earns a gate, and what does not

All three have to hold, or the check costs more than it returns:

1. **The two halves sit in different files or different languages**, so no
   compiler compares them.
2. **A drift is silent.** If it fails loudly — a build error, a red task, a
   line in `git status` — the gate is a second opinion nobody needs.
3. **It has already happened**, here, and is written down beside the check.

That leaves three things out on purpose. A gate over the SPELLING of the
implementation — a regex across Go source — fixes where the code is written
rather than what it does, and it breaks on a refactor that changed nothing: one
here read an awk program out of a workflow by regex, and moving that program
into its own file broke the test while the rule stayed identical. A gate over
TASTE is the author's preference, and belongs in a review or a linter. A gate
over NAVIGATION — an index of the documents, a table describing these gates —
protects a reader from ten seconds of `ls`.

Three were removed the day this section was written, for one of those reasons
each. Subtracting is the same work as adding.
