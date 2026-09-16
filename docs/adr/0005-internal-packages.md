# ADR-0005: The implementation is internal, and the releases are not library versions

**Status:** Accepted
**Deciders:** Oleg Tkachuk

## Context

The shared code lived in `pkg/`, a top-level directory whose packages Go
considered importable by anybody. Nothing outside this repository imported
them and nothing usefully could: `layer` is coupled to this chart registry and
this output contract, `hetzner` to this topology format.

Two facts forced the question. A removal — `Component.ValuesYAML`,
`Component.Values` and `ReleaseArgs.Values` — was typed as a `refactor`, which
cuts no release under the conventionalcommits preset, so no notes mentioned it;
whether that mattered depended on whether `pkg/` had consumers, which nothing
had ever decided. And no released version resolves anyway: the module path
carries no `/vN` suffix while the tags are past v1, so by Go's import
compatibility rule those releases are unreachable through `go get`. The tags
are releases of an infrastructure tree, which is what `release.config.cjs` says
they are.

## Decision

**`pkg/` is `internal/pkg/`.** Go refuses an import of `.../internal/...` from
outside this module, so the existing state is a property of the compiler rather
than a claim in a document.

**Grouped under `pkg/` rather than directly in `internal/`, because the
grouping carries meaning.** `internal/pkg/` is the IaC implementation, imported
by every layer, the cluster tier and the tools. `internal/ci/` is the
repository's own gates — tests about workflows, taskfiles and documentation,
imported by nothing. Two kinds of thing, two directories.

**The module path stays without a `/vN` suffix**, which keeps the tags releases
of a tree and the packages unconsumable.

## Consequences

- A removal from `internal/pkg/` cannot break an importer, because there can be
  none.
- The two groups are legible from the tree rather than from a document.
- Import paths are two segments longer.
- Publishing one of these later — `hetzner` is the only remotely reusable
  one — is a move plus its own `go.mod`. That was already true: as a library it
  would want to be a separate module, not a package in this tree.

**Cost paid once:** 68 files of imports, and six hard-coded paths in
`renovate.json`, `.golangci.yaml`, `lefthook.yml` and `ci.yaml`. Two of those
fail silently when stale — Renovate's chart manager and lefthook's glob — so
`TestRenovatePatterns_WatchPathsThatExist` was written before the move and
proved itself by failing on the post-move path.

## To revisit

If anything outside this repository ever needs `hetzner`, it becomes its own
module at its own version, and this ADR is superseded rather than amended.
