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

They run as part of the unit suite, so `task go:test`, `task verify` and CI all
carry them.

## What each guards

| Test file | Guards |
|-----------|--------|
| `addresses_test.go` | no tracked file names a real IP address of this project's own; examples stay in RFC 5737 space |
| `adr_test.go` | each record's status and heading against the row the index gives it, both ways |
| `bots_test.go` | one bot updates dependencies, so two do not open the same pull request |
| `componentoutputs_test.go` | a component's registered outputs against the fields they are tagged as |
| `configkeys_test.go` | each layer's declared config against the config its code reads, and both documentation tables |
| `dates_test.go` | no dates in prose — git already records when somebody learned something |
| `diagrams_test.go` | every layer a diagram names exists, and every diagram reference resolves |
| `discards_test.go` | every discarded error says why it is discarded |
| `domains_test.go` | no tracked file writes a real domain or ACME address as a value; examples stay in RFC 2606 space |
| `exits_test.go` | every main package reports a failure the same way, and none of them panics |
| `gatemarkers_test.go` | the † in the command reference against the tasks the workflows run |
| `gitignore_test.go` | every main package's binary name has a `.gitignore` entry |
| `gobuild_test.go` | every `go build` a taskfile or workflow runs names an output path |
| `ingressclass_test.go` | the retired ingress-class literal stays out of executed code; `internal/pkg/platform.IngressClass` is the one spelling |
| `layers_test.go` | every layer with a component set calls `layertest.Check` |
| `location_test.go` | the probe location and every yaml fixture name a location that exists |
| `modulepath_test.go` | go.mod against the README note saying nothing can import this |
| `outputs_test.go` | every stack output a taskfile reads is declared in Go, and every export is named |
| `platformnames_test.go` | a shared name is declared once, in internal/pkg/platform, not twice |
| `readmes_test.go` | every tool and every internal package documents itself, under its own heading |
| `renovate_schedule_test.go` | the Renovate schedule is not narrower than a day, which a best-effort cron cannot meet |
| `renovate_test.go` | Renovate's own regex, read from its own config, matches every annotated pin — and every pin is annotated |
| `renovatepaths_test.go` | the paths Renovate watches exist, which a rename breaks silently |
| `resourceoptions_test.go` | no `append` onto a shared option slice, which silently writes into the caller's array |
| `taskargs_test.go` | every task's arguments, guards and documented row |
| `tasklib_test.go` | every remote taskfile has the checksum Task will look for, ref included |
| `vendored_test.go` | each vendored manifest against its recorded digest, with no network |
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
