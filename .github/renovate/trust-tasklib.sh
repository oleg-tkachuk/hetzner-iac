#!/usr/bin/env bash
# Re-trust the shared task library after Renovate moves its ref.
#
# Task names each trust file under .task/remote for the sha256 of the remote
# URL, ref included, so a bump leaves every committed checksum pointing at a
# ref nothing fetches any more. Renovate runs this as a postUpgradeTask and
# commits what it changes.
#
# Every entry point is loaded, because each includes a different set of
# modules from the library.
set -euo pipefail

readonly remote_cache=".task/remote"

find "${remote_cache}" -name '*.checksum' -delete

for entry in Taskfile*.yaml; do
  task --yes --taskfile "${entry}" --list > /dev/null
done
