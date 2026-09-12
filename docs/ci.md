# How changes land

Everything goes through a pull request; `main` is protected and takes no direct
pushes.

```
branch → PR → CI → rebase merge → release
```

The pieces that make that work, and the reason each one is there:

- **Rebase is the only merge method.** Each commit of the pull request is
  replayed onto `main` as it was written, so the history stays the sequence of
  changes it actually was rather than one squashed lump.
- **Every commit is checked against Conventional Commits.** Under rebase they
  all land on `main`, and semantic-release reads each of them to pick the next
  version and write the notes. A commit that does not conform contributes
  nothing, and a pull request made entirely of them produces no release at all
  — silently. CI checks them with the same expression as the local commit-msg
  hook, which is opt-in per clone; the CI check is not.
- **Release is a job of the CI workflow**, gated by `needs:` on every check.
  It used to be a workflow of its own triggered on push, running in parallel
  with the checks — and v1.0.2 was cut from a commit whose CI was failing. The
  commit-message check is deliberately *not* in that `needs:` list: it only
  runs on pull requests, and a skipped dependency would skip the release along
  with it. It is enforced as a required check on the branch instead.
- **`main` requires linear history** and refuses force pushes, so the commit a
  release points at is the commit that was tested.

Release notes are generated from the commit history by semantic-release; there
is no changelog file to keep in step.

# Security scanning

The scanners live in their own workflow, `.github/workflows/security.yaml`,
which CI calls and which also runs weekly on its own. That schedule is the
reason for the split: a CVE published today makes yesterday's green commit
vulnerable, and a gate that only runs on push would never say so.

Each job installs its tool and then calls the same task an operator runs
locally, so the flags live in one place rather than being restated in YAML.
`task scan` is the whole set.

Accepted findings live in `.trivyignore.yaml` and `.checkov.yaml`, each with
the reason it stands. Entries are removed as soon as a fix lands — a stale
ignore masks the finding coming back.

**checkov** is the fourth, and it covers what the other three do not: 480-odd
hardening rules read against the Kubernetes manifests as Kubernetes, and
against the workflows as a build pipeline. trivy reports IaC misconfiguration
too, but report-only — its findings are dense — while zizmor and actionlint
read the workflows for security and for correctness rather than for hardening.
It runs on every pull request, pinned, in under three seconds.

Note that govulncheck and trivy disagree by design: govulncheck reports only
what this code can actually reach, trivy reports everything present in the
dependency graph. Both are useful, and a finding in one and not the other is
information rather than a contradiction.

## What runs when

Pull requests run everything cheap and everything that catches a mistake the
same day. One check runs nightly instead — **the race detector**, which
compiles the whole tree a second time with build IDs nothing else can reuse,
twelve minutes that would sit on the critical path of every pipeline.

Standalone gosec used to be nightly for the same stated reason and is back on
every pull request. Measured, it costs four minutes beside a `Tests and vet`
that takes thirteen, so it adds nothing to the wall clock — and it covers the
half golangci-lint cannot: that job runs gosec under `--new-from-merge-base`,
so a finding already on `main` is invisible to it for good. The two also
disagree on rules, which is what the `#nosec G204` in `tools/stack` is for.

A push to `main` runs almost nothing: the priming job, the scanners, `Tests
and vet`, and the release.

`Tests and vet` is there because branch protection does **not** require a
branch to be current before it merges. That requirement cost a second full run
of everything on every pull request — update the branch, the checks start
again — and bought only the guarantee that the pull request had compiled
against the exact `main` it landed on. This job buys the same guarantee for a
quarter of the price: without `strict`, the tree on `main` after a merge is a
combination no pull request ever compiled — one renames a function, another
adds a caller, both green apart and broken together — so the merged tree is
compiled and tested once, warm. The other checks stay pull-request-only
because they read one tree rather than a combination.

Neither the cluster tier nor the e2e suite is previewed in CI, and both
absences are deliberate:

- the cluster tier needs the gitignored topology file, which a runner does not
  have;
- the e2e suite needs to reach the Kubernetes API, which the firewall opens to
  `network.adminCIDRs` only. GitHub's published runner ranges are 6980 CIDRs
  of shared Azure that rotate, so allowing them would put everyone able to run
  a workflow inside the perimeter.

Both are checked by hand: `task cluster:plan` and `task e2e`.

## Why the pipeline is not slow any more

Every run used to pay for a cold compile of a 208-module graph dominated by the
generated Pulumi Kubernetes SDK. Two separate causes.

**The shared Go cache was poisoned.** `actions/setup-go` caches the module and
build directories under a key derived from `go.sum`, and that key is immutable:
whichever job saves first owns it for good. Early runs failed before they
touched Go, saved a 31 MB cache, and every later run restored those 31 MB and
rebuilt everything.

The roles are explicit now: `.github/actions/setup-go` takes a `cache-mode`,
the priming job is the only writer and only on a push to `main`, everything
else is `restore`, and a race build opts out entirely because its artifacts
carry build IDs nothing else can reuse. CI asserts that exactly one writer
exists, because a comment did not prevent the second occurrence.

The priming job builds unconditionally. The first version skipped the build
when no Go had changed, saved an empty cache under the immutable key, and
poisoned it again on the very run that introduced it. It is also push-only: on
a pull request `cache-mode` resolves to `restore`, so the job compiled for 277
seconds and wrote nothing, while the four jobs waiting on it restored the same
cache from `main` they would have restored anyway.

**gosec's memory.** It loads every package with full syntax *and* type
information for the whole transitive graph, and processes `-concurrency` of
them at once — defaulting to the core count, so fourteen large graphs at once
on a developer machine. `GOSEC_FLAGS: -concurrency=4` in the Taskfile caps it.

A docs-only change still reports every check: the jobs run and skip their
expensive step, rather than being skipped themselves. A required check that
never reports leaves the pull request waiting forever.

# Chart upgrades arrive as pull requests

Every chart is pinned in [pkg/charts/registry.go](../pkg/charts/registry.go) —
one file, no version literal anywhere else. Renovate watches it through a regex
in [.github/renovate.json](../.github/renovate.json) and opens one pull request
per chart, weekly, labelled `charts`, with `fix(charts):` so the upgrade
reaches a release (`feat(charts):` for a major, so the version says so).

It runs from
[.github/workflows/renovate.yaml](../.github/workflows/renovate.yaml) rather
than as the hosted GitHub App, which needed account rights that were not
available. Dependabot was the other option and does not fit: it reads gomod and
github-actions natively but cannot see a chart version pinned inside a Go
source file, which is the whole point here.

Two schedules, which is not a contradiction. The workflow's cron decides how
often Renovate **runs** — daily. `renovate.json` decides what it may **do**
when it runs: regular updates wait for Monday so chart upgrades batch into one
review, while `vulnerabilityAlerts` are exempt and can land any morning. A
weekly cron alone would have delayed a security fix by up to seven days.

`workflow_dispatch` runs a pass now and sets `RENOVATE_FORCE` to ignore the
Monday schedule, which is how the first pass happens without waiting for it.

Self-hosting costs a token. `GITHUB_TOKEN` cannot serve: a pull request opened
with it does not trigger `pull_request` workflows, so no required check would
ever report and branch protection would block the merge for ever. The workflow
fails with that explanation, and the scopes, when `RENOVATE_TOKEN` is unset.

Two things about that are worth knowing before a bot's pull request arrives.

**Renovate cannot maintain `AppVersion`.** The helm datasource knows chart
versions and nothing else, so a bumped pin sits beside an app version the chart
no longer ships. `task charts:appversions` reads each repository's index and
fails when they disagree — a misleading comment for most charts, and a real
defect for alloy, whose validation image tag is built out of it. The pull
request says so in its own body, and the fix is the value that check prints.

**The regex is a silent failure waiting to happen.** It keys off the field
order `Name → Repo → Version`; reorder them and Renovate stops matching, opens
no pull request, and reports nothing. So `tools/charts` reads that regex out of
Renovate's own configuration and asserts it still matches every chart in the
registry. Verified by reordering two fields on purpose:

```
Renovate's pattern does not match chart "loki" (key "loki") — it would never be upgraded
```
