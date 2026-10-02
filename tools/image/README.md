# image

Bakes the Talos snapshot the topology names into the Hetzner project. It is
idempotent: an existing snapshot for that version **and** architecture is left
alone.

```bash
go run ./tools/image <topology.yaml> <stack>     # bake, unless present
go run ./tools/image installer <topology.yaml>   # the installer image an upgrade uses
```

Images come from the [Talos Image Factory](https://factory.talos.dev): the
schematic id is content-addressed, so there is no hash to keep in sync. The
schematic goes through Sidero's
[Image Factory client](https://github.com/siderolabs/image-factory/tree/main/pkg/client),
and the snapshot check through `hcloud-go`. The upload itself is
`hcloud-upload-image`, which creates a server, writes the image to its disk and
snapshots it.

Needs the stack's Hetzner token and `hcloud-upload-image` in PATH. Run by
`task cluster:image:bake`.
