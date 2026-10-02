# charts

Reports on the chart pins in [internal/pkg/charts](../../internal/pkg/charts): what is pinned,
what upstream has moved past, whether each pin still renders the workloads
expected of it, and whether the `AppVersion` it ships is the one intended.

```bash
go run ./tools/charts list          # every pin, with namespace and repo
go run ./tools/charts outdated      # each pin against the latest upstream
go run ./tools/charts render        # the pins still produce their workloads
go run ./tools/charts appversions   # each AppVersion is what the pin ships
go run ./tools/charts repin         # move each unsigned image's digest pin to its chart's tag
```

`render` is the one with teeth. A chart upgrade that renames a Deployment does
not fail `pulumi up`; it fails the e2e suite against a real cluster later. This
runs `helm template` at the pinned versions, compares the workloads, checks
that the values took effect — `helm template` renders a misspelt value and
exits zero — and validates the output with `kubeconform` against the
Kubernetes version the topology pins.

It also holds every image to [the image inventory](../../internal/pkg/imagepolicy/images.yaml),
and every unsigned image to its digest pin. A chart bump that moves an
unsigned image's default tag fails here until the pin moves with it, which
`repin` does; Renovate runs it on every chart bump.

It renders with `--repo` rather than `helm repo add`, so a read-only check does
not mutate the operator's Helm configuration. `render` needs `helm` and
`kubeconform` in PATH, and network: kubeconform fetches the schemas.

Run by `charts:list`, `charts:outdated`, `charts:render-check`,
`charts:appversions`, and by CI.
