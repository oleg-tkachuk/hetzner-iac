# internal

Packages that belong to this repository and are not importable from outside
it — Go enforces that for any path containing `internal`.

| Group | Holds |
|-------|-------|
| [pkg/](pkg) | the implementation: Pulumi components, contracts, chart pins, values |
| [ci/](ci) | the repository's own gates: the cross-file contracts nothing else compares |

Two directories because they are two kinds of thing. `pkg/` is what the
programs are built from — imported by every layer, the cluster tier and the
tools. `ci/` is imported by nothing: test files about workflows, taskfiles and
documentation.

The distinction from [tools/](../tools) is different again, and it is what a
directory *is* rather than who runs it. A tool is a command, with a `main` and
something to run. Neither group here has one.
