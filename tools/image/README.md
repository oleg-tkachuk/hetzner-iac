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
customisation is deterministic and there is no hash to keep in sync. Note the
factory answers `POST /schematics` with **201**, not 200 — demanding 200 meant
no image could be baked at all.

What stays external is the upload: `hcloud-upload-image` creates a server,
writes the image to its disk and snapshots it. Importing that as a library
would tie this repository to its API for one process invocation.

Run by `task cluster:image:bake`.
