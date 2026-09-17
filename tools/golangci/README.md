# golangci

Runs golangci-lint at the version CI pins, and refuses to run a different one.

```bash
go run ./tools/golangci run ./...      # what task lint does
task lint:install                      # writes the pinned copy into bin/
```

The version comes from `GOLANGCI_VERSION` in `.github/workflows/ci.yaml` — the
same value CI installs from, read rather than copied, so there is one thing to
bump and Renovate already bumps it.

The binary is `bin/golangci-lint` when that exists, and whatever PATH offers
otherwise. That order is the point.

## The failure it exists for

A `go install`-built golangci-lint in `~/go/bin` shadowed Homebrew's. The two
were 2.12.2 and 2.13.2, CI pins 2.13.2, and the older one reported eight
`goconst` findings on an untouched `main`:

    tools/etcd/main.go:75:34: string `verify` has 6 occurrences (goconst)

`task go:lint` was red, the pull request was green, and the pre-push hook
refused to push work that was fine. Nothing in either message named a version,
so the only way to find it was `which -a`.

A linter is not a matter of taste about which copy runs: two versions report
different findings, and the one CI runs is the one that decides whether a
change lands.
