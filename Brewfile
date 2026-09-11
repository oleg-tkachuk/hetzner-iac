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
# workloads pkg/workloads declares, offline.
brew "helm"

# Used by the image-bake task.
brew "jq"

# --- Required, but read the note --------------------------------------------

# Day two: upgrades, etcd snapshots, and `task cluster:config-check`.
#
# That check refuses a talosctl whose minor does not match the topology's
# talos.version, because a mismatched binary reports conflicts that will not
# happen. Homebrew currently ships 1.14 while the topology pins v1.13.10, so
# `brew install talosctl` gives a binary config-check will decline to use.
#
# Install the matching minor instead when you need that check:
#
#   curl -sLo /usr/local/bin/talosctl \
#     https://github.com/siderolabs/talos/releases/download/v1.13.10/talosctl-darwin-arm64
#
# Left here because every other talosctl use is version-tolerant.
brew "talosctl"

# --- Optional: only the tasks that name them ---------------------------------
#
# Each of those tasks says what to install rather than skipping itself
# silently, so a clone without these is not a broken clone.

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
