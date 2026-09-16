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

It replaces pipelines. `df --output=avail -BM / | tail -1 | tr -dc '0-9'` is
three processes and three chances to return the wrong thing quietly; `avail-mb`
is one. The same argument retires every `grep | grep -c` pair, because awk
counts while it matches. `staged-binaries` asks **git** what is binary rather
than the `file -b | grep -qE 'ELF|Mach-O'` it replaced, which knew two formats
and let a PNG, a gzip and an ar archive through.

What it deliberately does not do is read YAML: that is
[topology get](../topology), which asks the parser the cluster is built from.
An awk program with an indentation state machine would still be a hand-rolled
YAML reader — wrong on a quoted value, an anchor, or a nested key of the same
name.

A missing or unknown op exits 2 rather than printing nothing, which is the
failure mode the pipelines had. Every op is covered by `main_test.go` beside it.

Run by CI, the `free-disk` composite action, the pre-commit hook, and
`task platform:destroy layer=all`.
