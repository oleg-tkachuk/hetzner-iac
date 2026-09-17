# charts

Reports on the chart pins in [internal/pkg/charts](../../internal/pkg/charts): what is pinned,
what upstream has moved past, whether each pin still renders the workloads
expected of it, and whether the `AppVersion` it ships is the one intended.

```bash
go run ./tools/charts list          # every pin, with namespace and repo
go run ./tools/charts outdated      # each pin against the latest upstream
go run ./tools/charts render        # the pins still produce their workloads
go run ./tools/charts appversions   # each AppVersion is what the pin ships
```

`render` is the one with teeth. A chart upgrade that renames a Deployment does
not fail `pulumi up` — the release installs, and the e2e suite fails against a
real cluster hours later. This runs `helm template` at the pinned versions and
compares, offline, in seconds. It also checks that the values took effect,
which was proven by breaking it: shortening `kubeProxyReplacement` by a letter
turns the check red, while `helm template` renders the typo and exits zero.

It renders with `--repo` rather than `helm repo add`, so a read-only check does
not mutate the operator's Helm configuration.

Run by `charts:list`, `charts:outdated`, `charts:render-check`,
`charts:appversions`, and by CI.
