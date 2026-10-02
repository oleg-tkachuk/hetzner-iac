# lines

One awk program for the line-oriented parsing this repository needs, selected
with `-v op=<name>`. Every op takes lines on stdin — `df` output, a git
numstat, a layer list, workflow files — and answers one question about them.

```bash
df --output=avail -BM / | awk -f tools/lines/main.awk -v op=avail-mb
git diff --cached --numstat | awk -f tools/lines/main.awk -v op=staged-binaries
awk -f tools/lines/main.awk -v op=cache-writers .github/workflows/*.yaml
awk -f tools/lines/main.awk -v op=reverse-words <<<"$layers"
```

| op | Answers |
|----|---------|
| `avail-mb` | megabytes free, from `df --output=avail -BM` |
| `cache-writers` | which workflow jobs write the Go build cache |
| `reverse-words` | the words in reverse, for destroying layers in reverse order |
| `staged-binaries` | the staged paths git considers binary, non-zero if any |

One process per question instead of a pipeline, so there is one place to be
wrong and a test for it. `staged-binaries` asks **git** what is binary rather
than guessing from `file` output.

It does not read YAML: that is [topology get](../topology), which asks the
parser the cluster is built from. An awk indentation state machine would be a
hand-rolled YAML reader.

A missing or unknown op exits 2 rather than printing nothing. Every op is
covered by `main_test.go` beside it.

Run by CI, the `free-disk` composite action, the pre-commit hook, and
`task platform:destroy layer=all`.
