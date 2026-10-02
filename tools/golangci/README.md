# golangci

Runs golangci-lint at the version CI pins, and refuses to run a different one.

```bash
go run ./tools/golangci run ./...      # what the lint task does
task -t Taskfile.dev.yaml lint:install    # writes the pinned copy into bin/
```

The version comes from `GOLANGCI_VERSION` in `.github/workflows/ci.yaml` — the
same value CI installs from, read rather than copied, so there is one thing to
bump and Renovate already bumps it.

The binary is `bin/golangci-lint` when that exists, and whatever PATH offers
otherwise. Any other version is refused because two versions report different
findings: a stray copy in PATH would turn the local lint and the pre-push hook
red on code CI passes, with nothing in the output naming a version.
