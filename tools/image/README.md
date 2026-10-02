# image

Bakes the Talos snapshot the topology names into the Hetzner project, and is
idempotent: an existing snapshot for that version **and** architecture is left
alone.

```bash
go run ./tools/image <topology.yaml> <stack>
```

It replaced a shell block of forty-eight lines and five tools — `curl | jq`
for the Image Factory, `hcloud image list | awk` for the idempotence check, a
`case` for the architecture, and a Taskfile `env:` stanza that resolved the
token before the task's own preconditions could run. That last one is a trap
this repository walked into: Task evaluates `env` first, so the precondition
holding the remedy was unreachable.

Images come from the [Talos Image Factory](https://factory.talos.dev) rather
than GitHub releases: the schematic id is content-addressed, so posting the
customisation is deterministic and there is no hash to keep in sync.

Both calls go through the vendors' own Go clients: Sidero's
[Image Factory client](https://github.com/siderolabs/image-factory/tree/main/pkg/client)
for the schematic, and `hcloud-go` for the check that a snapshot already
exists. The schematic used to be a POST written here, which demanded a 200 the
factory never sends — it answers 201 — so no image could be baked at all until
a bake for a second architecture found it.

What stays external is the upload: `hcloud-upload-image` creates a server,
writes the image to its disk and snapshots it. Importing that as a library
would tie this repository to its API for one process invocation.

Run by `task cluster:image:bake`.
