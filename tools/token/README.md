# token

Prints the Hetzner API token for a stack on stdout.

```bash
export HCLOUD_TOKEN="$(go run ./tools/token dev)"
```

It exists so a taskfile can put the token in the environment of a tool that
reads it from there — the `hcloud` CLI — without restating how the token is
found. That resolution is `internal/pkg/hetzner.Token`: an exported `HCLOUD_TOKEN`
first, then the encrypted stack config through Pulumi's own decryption.

stdout and only stdout. The caller is `export HCLOUD_TOKEN="$(…)"`, so the
value never reaches an argument vector where `ps` would show it to every other
user on the machine. It is the same reason `task cluster:token` takes no
`token=` argument.

Run by the root taskfile, which hands `HCLOUD_TOKEN` to the shared library's
`hcloud` module, and by the cluster tasks that call `hcloud` directly.
