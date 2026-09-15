# internal

Packages that belong to this repository and are not importable from outside
it — Go enforces that for any path containing `internal`.

| Package | Holds |
|---------|-------|
| [ci/](ci) | the repository's own gates: the cross-file contracts nothing else compares |

The distinction from [tools/](../tools) is what a directory *is*, not who runs
it. A tool is a command, with a `main` and something to run. What is here has
neither.
