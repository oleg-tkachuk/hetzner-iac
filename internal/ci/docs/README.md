# docs

The gates that read documentation or `.gitignore`. Apart from [internal/ci](..)
because those paths are inert: a prose-only pull request skips the unit suite,
so these run in the `Documentation links` job, which ignores relevance.

```bash
task -t Taskfile.dev.yaml docs:check     # what CI runs
go test ./internal/ci/docs/ -run TestADRIndex -v
```

`go test ./...` carries them too, so every code change runs them as before.

| Test file | Guards |
|-----------|--------|
| `adr_test.go` | each record's status and heading against the row the index gives it, both ways |
| `configkeys_test.go` | both stack config tables name only keys a layer declares, and the reference names all of them |
| `dates_test.go` | no dates in prose — git already records when somebody learned something |
| `diagrams_test.go` | every layer a diagram names exists, and every diagram reference resolves |
| `entrypoints_test.go` | each documented command names the entry point that has the task, and the reference keeps the two in separate tables |
| `externaltools_test.go` | every binary this repository runs is named in the prerequisites, the checks' list or the Brewfile |
| `gatemarkers_test.go` | the † in the command reference against the tasks the workflows run |
| `gitignore_test.go` | every main package's binary name has a `.gitignore` entry |
| `identifiers_test.go` | no tracked file names a real address, domain or ACME contact of this project's own; examples stay in the ranges RFC 5737 and RFC 2606 reserve |
| `layers_test.go` | every `layers/NN-name` any file names is a layer that exists |
| `readmes_test.go` | every tool and every internal package documents itself; no README names a file that is not in the tree; the docs index lists every document |
| `renovatealerts_test.go` | vulnerabilityAlerts in renovate.json against the token permission docs/ci.md tells you to grant |
| `taskdocs_test.go` | every task a document tells somebody to run exists, and none is one the library include leaves out |
| `weight_test.go` | this package links no Pulumi, because the docs job builds it without a cache |
| `root_test.go` | — where the repository root is from here |
