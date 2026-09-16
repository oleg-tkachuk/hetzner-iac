# Prerequisites, for macOS. `brew bundle` installs them.
#
# Verified against Homebrew rather than written from memory: every formula here
# exists, and the one that does not is noted at the bottom instead of being
# guessed at.
#
# Three groups, and the split is the one the README makes: what a cluster needs,
# what one task needs, and what the checks need. Only the first is in the
# README's prerequisites — the rest are for running CI's own checks locally.

# --- Required: building and running a cluster --------------------------------

# Runs everything here. The pin in every Pulumi.yaml is the floor, not this.
brew "pulumi"

# The programs are Go.
brew "go"

# The entry points. Remote Taskfiles need 3.53, which is why the formula is
# `go-task` and not `task` — the latter is a different project.
brew "go-task"

# Inspection, and baking the Talos image.
brew "hcloud"

# The status tasks read the cluster with it, and tools/orphans asks it what
# still exists.
brew "kubernetes-cli"

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

# --- Required by one task ----------------------------------------------------
#
# task cluster:etcd:upload. restic does the upload, the retention and the
# integrity check; rclone is only its transport, because restic's own sftp
# backend speaks key authentication and the Storage Box credential is a
# generated password.
brew "restic"
brew "rclone"

# --- The checks, which build nothing -----------------------------------------
#
# docs/ci.md lists these with the check each one serves. Every task states what
# to install rather than skipping itself silently, so a clone without them is
# not a broken clone.

# Chart rendering: `task charts:render-check` proves a chart still produces the
# workloads internal/pkg/workloads declares.
brew "helm"

# Schema validation of what those charts render, against the Kubernetes version
# the topology pins. It fetches the schemas, so this one needs egress.
brew "kubeconform"

brew "lychee"        # task docs:links
brew "golangci-lint" # task security:lint
brew "gitleaks"      # task security:secrets
brew "gosec"         # nightly, and task security:gosec
brew "trivy"         # task security:trivy
brew "lefthook"      # the commit and push hooks; opt in with `lefthook install`
brew "actionlint"    # workflow syntax, the same check CI runs
brew "zizmor"        # workflow permissions, the same check CI runs

# --- Not in Homebrew ---------------------------------------------------------
#
# hcloud-upload-image — Hetzner has no custom-image upload API, and this is the
# tool that works around it. Required, and a Go install, which is why the
# README says `brew bundle` covers everything but this one:
#
#   go install github.com/apricote/hcloud-upload-image@latest
#
# curl and git ship with macOS, so neither is listed. A newer curl is in
# Homebrew if you want one, but nothing here needs it.
