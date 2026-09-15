# Prerequisites, for macOS. `brew bundle` installs them.
#
# Verified against Homebrew rather than written from memory: every formula here
# exists, and the two that do not are noted at the bottom instead of being
# guessed at.

# --- Required ---------------------------------------------------------------

# Runs everything here. The pin in every Pulumi.yaml is the floor, not this.
brew "pulumi"

# The programs are Go.
brew "go"

# The entry points. Remote Taskfiles need 3.53, which is why the formula is
# `go-task` and not `task` — the latter is a different project.
brew "go-task"

# Inspection, and baking the Talos image.
brew "hcloud"

# Chart rendering: `task charts:render-check` proves a chart still produces the
# workloads pkg/workloads declares.
brew "helm"

# Schema validation of what those charts render, against the Kubernetes version
# the topology pins. It fetches the schemas, so this one needs egress.
brew "kubeconform"

# Two JSON field reads in the status tasks. image-bake used to need it and
# no longer does — tools/image parses with encoding/json.
brew "jq"

# --- Required, but read the note --------------------------------------------

# Machine-config validation, upgrades, etcd snapshots.
#
# Install this one and keep it. Homebrew carries only the newest talosctl,
# which is a minor ahead of the topology's talos.version — and
# `task cluster:machine-config:check` refuses a mismatched minor, because a
# binary from another minor reports conflicts that will not happen and misses
# real ones.
#
# Both can coexist, and no PATH surgery is needed:
#
#   task cluster:talosctl:install
#
# writes the pinned version into bin/talosctl, which that check prefers over
# PATH. The version comes from the topology and the platform from uname, so it
# stays right on Linux and after the pin moves. This note used to carry a curl
# with v1.13.10 and darwin-arm64 written into it, which was neither.
#
# Every other talosctl use here is version-tolerant, which is why brew's newest
# is fine on PATH. The pin is not caution: pulumi-talos v0.8.1 is the newest
# release, and the provider it bridges embeds Talos machinery v1.13.0 — the
# thing that GENERATES the machine config.
brew "talosctl"

# --- Optional: only the tasks that name them ---------------------------------
#
# Each of those tasks says what to install rather than skipping itself
# silently, so a clone without these is not a broken clone.

# task cluster:etcd:upload. restic does the upload, the retention and the
# integrity check; rclone is only its transport, because restic's own sftp
# backend speaks key authentication and the Storage Box credential is a
# generated password.
brew "restic"
brew "rclone"

brew "golangci-lint" # task security:lint
brew "gitleaks"      # task security:secrets
brew "gosec"         # nightly, and task security:gosec
brew "trivy"         # task security:trivy
brew "lefthook"      # the commit and push hooks; opt in with `lefthook install`
brew "hadolint"      # task security:dockerfile
brew "actionlint"    # workflow syntax, the same check CI runs
brew "zizmor"        # workflow permissions, the same check CI runs

# --- Not in Homebrew ---------------------------------------------------------
#
# hcloud-upload-image — Hetzner has no custom-image upload API, and this is the
# tool that works around it. Go install, and it needs the `go` above:
#
#   go install github.com/apricote/hcloud-upload-image@latest
#
# curl and git ship with macOS, so neither is listed. A newer curl is in
# Homebrew if you want one, but nothing here needs it.
