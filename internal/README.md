# internal

Packages that belong to this repository and are not importable from outside
it — Go enforces that for any path containing `internal`.

| Group | Holds |
|-------|-------|
| [pkg/](pkg) | the implementation: Pulumi components, contracts, chart pins, values |
| [ci/](ci) | the repository's own gates: the cross-file contracts nothing else compares |

`pkg/` is what the programs are built from — imported by every layer, the
cluster tier and the tools. `ci/` is imported by nothing: test files about
workflows, taskfiles and documentation. Neither has a `main`; commands live in
[tools/](../tools).
