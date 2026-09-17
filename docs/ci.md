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
- **Release is [`release.yaml`](../.github/workflows/release.yaml)**, dispatched
  by CI once every check on `main` has passed. It has been all three shapes and
  the history is the argument: as a workflow on `push` it ran in parallel with
  the checks and v1.0.2 was cut from a commit whose CI was failing; as a
  `workflow_call` job of CI it could not ship from a red pipeline but could
  never accumulate a run of its own, because a called workflow executes inside
  its caller. CI now dispatches it, so the gate stays in `needs:` and the
  release gets its own run. The commit-message check is not part of that
  guarantee: it runs only on pull requests, so it is a required check on the
  branch instead.
- **`main` requires linear history** and refuses force pushes, so the commit a
  release points at is the commit that was tested.

Release notes are generated from the commit history by semantic-release; there
is no changelog file to keep in step.

## Why a dependency update is the slow case

No job writes a build cache on a pull request — `prime` is push-only, because a
cache written on a pull request is scoped to that pull request's ref and no
other branch could read it. So every pull-request job restores the last cache
from `main`.

That is free until `go.sum` changes. Then the exact key misses, the restore-key
prefix hands back the *previous* dependencies, and anything that type-checks the
module has to compile the difference — which on a `go:deps:update` pull
request is close to a full rebuild. Measured on one:

| Job | Usually | On a deps update | Bound |
|---|---|---|---|
| `Tests and vet` | ~4m | **10m52s** | 20m |
| `Go lint` | ~2m | **9m35s** | 20m |
| `Insecure patterns` (gosec) | ~5m, of which gosec is 23s | **killed twice** | 15m → **25m** |

gosec's cost is not gosec: it type-checks the whole transitive graph, so its
runtime is whatever the cache does not hold — 23 seconds warm, the same work as
`Tests and vet` cold, plus the cache restore and its own install before that
starts. Hence the larger bound, which does not make it faster; it makes the
cause readable, because both failures arrived as `exit status 143` with a
truncated log. The job prints the runner's cores and memory first now.

### gosec is downloaded, not compiled

`go install` built gosec from source on every run: **1m06s, 1m08s, 1m16s warm
and 1m22s cold**, measured across four runs — against a gosec step that is
23-27s. The install cost three times the analysis it enabled.

It never got faster with a warm cache, and the reason is specific: the cache
holds *this* module's build. gosec's own dependencies are not in this module's
graph, so nothing that primes the cache ever compiles them and they are rebuilt
every time.

The verification is replaced rather than dropped. `go install` checked the
module against Go's checksum database; the download checks the tarball against
the release's own `checksums.txt`. That is weaker — it binds the artifact to
that release manifest rather than to an out-of-band trust root — and still
stricter than the two downloads already here, since `kubeconform` and
`talosctl` are fetched with a bare `curl` and verified against nothing. gosec
also publishes a Sigstore bundle, which is stronger and needs `cosign` on the
runner.

### The version stays pinned

`GOSEC_VERSION` is a pin, and fetching "whatever is newest" each run would be a
worse idea here than for most tools. gosec is a linter: a new release adds
rules, so a pull request would go red with no change of its author's, and
nothing in the failure would say which. This repository already made that
argument about a different tool, in `ci.yaml` — "`latest` would let a
kubeconform release change what CI accepts with no commit of ours".

Checked when they were pinned: all nine tool versions were the current
upstream releases, so nothing was stale. Keeping them that way is now Renovate's job.

A second custom manager watches `.github/workflows/*.yaml`, and each pin carries
the annotation it reads:

```yaml
# renovate: datasource=github-releases depName=securego/gosec
GOSEC_VERSION: "v2.29.0"
```

The annotation holds the datasource because the nine are not homogeneous —
GitHub releases for five, PyPI for `checkov` and `zizmor`, Go modules for
`govulncheck` and `actionlint` — and one pattern reading a comment beats one
pattern knowing nine special cases.

`extractVersion` appears on the three whose pin omits the `v` their upstream
tag carries: `kubeconform`, `gitleaks` and `trivy` are pinned as `0.8.0` while
the tags read `v0.8.0`, because the workflows add the `v` themselves in the
download URL. Without it Renovate compares `0.8.0` against a list of `v…` and
finds nothing to do — a bot that looks broken while working exactly as told.

Three tests in `internal/ci` guard it, for the same reason the chart manager has
one: the failure is silent. Renovate does not error on a pin it cannot match —
it opens no pull request, for ever. They run Renovate's own regex out of its own
configuration and assert that every pin is matched, that every datasource is one
of the three, and that `extractVersion` is present exactly where the `v` is
missing. Each was checked by breaking it on purpose.

### How many cores, and what that decides

The work is 1 916 CPU-seconds, measured locally. What turns that into minutes
is the runner, and the runner changed when this repository went public:
measured on a run after the switch, `nproc` says **4 cores and 15 Gi**, against
the 2 cores and 8 GB a standard runner gets on a private repository. The job
prints both before it starts, so the next reader measures this rather than
looking it up.

| | cores | scaled from 1 916 CPU-s |
|---|---|---|
| `ubuntu-slim` | 1 | ~32m |
| `ubuntu-latest`, private repository | 2 | ~16m |
| `ubuntu-latest`, public — **this one** | 4 | ~8m |
| larger runner | 8 | ~4m, and not open to a personal account |

Scaling by cores assumes the work parallelises, which Go compilation largely
does; treat the figures as the shape of the answer rather than a promise.

The bound on that job stays at 25 minutes. What it was raised for is a cold
cache on a dependency update — the case above — and halving the compile time
does not remove it.

`GOSEC_FLAGS` is `min(4, cores)`, computed rather than assumed. It was a flat
`-concurrency=4` with a comment calling four "the CI runner's core count",
which oversubscribed two cores twofold on the private runner: that cannot add
throughput and can only add memory pressure.

## Renovate runs when you ask it to

Two triggers decide *how often* Renovate runs, and `renovate.json` decides what
it may *do* once running. Getting that pair wrong is silent, and it was:

### Its token, and why vulnerability alerts are off

`RENOVATE_TOKEN` is a personal access token, not `GITHUB_TOKEN`: a pull request
opened with the latter starts no `pull_request` workflow, so no required check
would ever report and branch protection would block every upgrade. The workflow
states the permissions it needs and refuses to start without the secret.

`vulnerabilityAlerts` is **off**, and that is a decision rather than an
oversight. Reading those alerts needs `Dependabot alerts: Read-only` on a
fine-grained token — or the `security_events` scope on a classic one — which
this token does not have. While it was enabled without that access, every run
logged

    WARN: Cannot access vulnerability alerts.

and carried on: the feature was configured, believed, and dead. A setting that
is off says what it does; one that is on and unreachable does not.

What still reports a vulnerability: Dependabot alerts on the repository itself,
govulncheck in *Reachable vulnerabilities*, and trivy over `go.sum` and the
manifests. What is lost is the fast path — a security fix no longer overrides
the schedule, so it arrives with Monday's batch.

Turning it back on is one change, not two: grant the permission and flip the
setting together. `TestRenovateAlerts_AgreeWithWhatTheTokenIsToldToAllow`
refuses either half alone.

- `schedule` in `renovate.json` was `before 09:00 on monday`, which with
  `timezone: Europe/Kyiv` is **Sunday 21:00 to Monday 06:00 UTC**;
- the workflow's cron is `0 6 * * *` — **06:00 UTC**, exactly as that window
  shuts.

GitHub runs scheduled workflows best-effort on shared runners. One Monday pass
started at **11:49 UTC**, five hours and forty-nine minutes late,
found three updates and filed all three under *Awaiting Schedule*. Renovate had
opened no pull request in this repository, ever.

`renovate-config-validator` accepts the broken schedule and the working one
identically — all four candidate forms pass — so validation was never going to
catch it. The schedule is now the whole of Monday: batching updates into one
weekly review is what the window was for, and the hour only made it depend on a
cron being punctual. A test in `internal/ci` refuses an hour-narrow schedule.

### A ticked checkbox acts at once

Ticking a box on the Dependency Dashboard records a **request**; Renovate
fulfils it on its next run. So the workflow also triggers on `issues: edited`,
filtered to the dashboard.

That trigger has a loop in it. Renovate rewrites the dashboard on every pass
using `RENOVATE_TOKEN` — a personal access token, which it has to be, because a
pull request opened with `GITHUB_TOKEN` does not trigger `pull_request`
workflows and CI would never run on an upgrade. Events from a PAT **do** start
workflow runs, so Renovate's own edit fires the trigger.

Filtering by actor cannot close it: the PAT acts as the person who owns it, so
Renovate's edit and a human's have the same sender. What separates them is what
changed — a guard step proceeds only when a checkbox carrying a Renovate marker
went from unchecked to checked, and Renovate un-ticks after acting, so its own
rewrite never adds one. The guard was checked against all four cases, including
Renovate re-rendering the dependency list and a tick on a non-Renovate line.

## Security scanning

The scanners live in their own workflow, `.github/workflows/security.yaml`,
which CI calls and which also runs weekly on its own. That schedule is the
reason for the split: a CVE published today makes yesterday's green commit
vulnerable, and a gate that only runs on push would never say so.

Each job installs its tool and then calls the same task an operator runs
locally, so the flags live in one place rather than being restated in YAML.
`task -t Taskfile.dev.yaml scan` is the whole set.

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

## The entry point the jobs call

Every step that runs a task passes `-t Taskfile.dev.yaml`:

```
- run: task -t Taskfile.dev.yaml security:gosec
```

The checks are on a second entry point because `task` on its own is the list
an operator reads, and a third of it was work they never run. Task finds
`Taskfile.yaml` by itself and offers no environment variable for a second
file, so the flag is the whole mechanism — and forgetting it is quiet: the
step resolves against the root entry point, which either fails with "does not
exist" or, for a name both files declare, runs the wrong task.
TestWorkflows_CallTasksThroughTheDevTaskfile refuses a step without it, and
TestRootTaskfile_HoldsNoCheck refuses a check added back to the root.

It is still the same task an operator runs; the flag names where it lives, not
a second copy of it. Two of these tasks read a pinned version out of this
workflow — `GOLANGCI_VERSION` and `CHECKOV_VERSION` — which is the other
reason they are not on the entry point a clone uses to apply a cluster.

## Tools the checks need

None of these builds a cluster, which is why the README's prerequisites leave
them out. CI installs them; a clone needs them only to run the same checks
locally. On macOS `brew bundle` installs every one.

| Tool | The check that wants it |
|------|-------------------------|
| `helm` | `charts:render-check` — renders each chart and compares it against what `internal/pkg/workloads` declares it produces |
| `kubeconform` | `charts:validate` — validates what those charts render against the Kubernetes version the topology pins |
| `lychee` | `task -t Taskfile.dev.yaml docs:links` |
| `golangci-lint` | `task -t Taskfile.dev.yaml lint`, which runs the version this workflow pins and refuses another — `task -t Taskfile.dev.yaml lint:install` writes it into `bin/` |
| `gitleaks` | `security:secrets` |
| `gosec` | `security:gosec`, and the nightly run |
| `trivy` | `security:trivy` |
| `checkov` | `task -t Taskfile.dev.yaml checkov:scan`, and `pipx` when checkov itself is not installed — the shared module runs the pinned version through it, reading the version out of this workflow |
| `actionlint` | workflow syntax — run by hand, the same check CI runs |
| `zizmor` | workflow permissions — the same |
| `lefthook` | the commit and push hooks, opt in per clone with `lefthook install` |

Each task states what to install rather than skipping itself silently: a gate
that skips itself when a tool is absent is a gate that quietly stops running.

## What runs when

Pull requests run everything cheap and everything that catches a mistake the
same day. One check runs nightly instead — **the race detector**, which
compiles the whole tree a second time with build IDs nothing else can reuse,
twelve minutes that would sit on the critical path of every pipeline.

**Nothing waits for the scanners.** Every compiling job used to name
`security` in its `needs:`, which put five scanners on the critical path of
the six slowest jobs — and each does seconds of work: gitleaks two, trivy
fifteen, checkov twenty-six. The wait bought fail-fast, which on a public
repository buys politeness to a free queue and pays for it in time to
feedback. They are required checks, so a red scanner still blocks the merge;
the one place the dependency is load-bearing is `release`, which keeps it,
because v1.0.2 was once cut from a commit whose CI was failing.

Standalone gosec used to be nightly for the same stated reason and is back on
every pull request. Measured, both it and `Tests and vet` now take about four
minutes and run in parallel, so it adds nothing to the wall clock — and it
covers the
half golangci-lint cannot: that job runs gosec under `--new-from-merge-base`,
so a finding already on `main` is invisible to it for good. The two also
disagree on rules, which is what the `#nosec G204` in `tools/stack` is for.

A push to `main` runs almost nothing: the priming job, the scanners, `Tests and
vet`, and a job whose only work is to dispatch the release. The release itself
follows as a run of its own.

The release is [`release.yaml`](../.github/workflows/release.yaml), triggered by
a `repository_dispatch` that CI sends after the checks pass.

That indirection exists to satisfy three requirements at once, and every
simpler answer fails one of them.

| | own run | red pipeline can't ship | no dangerous trigger | no extra credential |
|---|---|---|---|---|
| `push` (the original) | ✔ | ✖ — v1.0.2 shipped red | ✔ | ✔ |
| `workflow_call` job of CI | ✖ | ✔ | ✔ | ✔ |
| `workflow_run` | ✔ | ✔ | ✖ — zizmor high | ✔ |
| tag push from CI | ✔ | ✔ | ✔ | ✖ — needs a PAT |
| **`repository_dispatch`** | ✔ | ✔ | ✔ | ✔ |

Two rows need their reasons stated.

`workflow_run` is the obvious answer and is refused. zizmor's
`dangerous-triggers` audit flags it categorically, and its documentation says no
guard satisfies it — "checking `github.repository` is not effective on
`workflow_run`, since a `workflow_run` always runs in the context of the target
repository". That would mean this repository's first silenced *high* finding,
bought for a populated page. `.github/zizmor.yml` turns off exactly one audit
today, for a style rule that could not be verified; that is the bar.

A tag pushed from CI looks like it should chain a `push: tags:` workflow and
does not. GitHub does not start a workflow run from an event triggered by
`GITHUB_TOKEN`, which is what makes the usual advice a long-lived PAT —
`repository_dispatch` is one of the two documented exceptions that always
create a run, so CI can dispatch with the token it already holds.

The gate is unchanged and still in `ci.yaml`: the dispatching job carries
`needs: [prime, security, build]` and the push-to-`main` `if:`, so it never
starts on a red pipeline and the dispatch is never sent. `release.yaml`
deliberately has **no** `workflow_dispatch` — a button that releases from an
untested `main` is how v1.0.2 happened.

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

Nothing is previewed or applied in CI, and no workflow holds a Pulumi token.
A public repository that previewed one operator's stacks would need their
credentials and their stack names in it; the checks here read the tree
instead.

So drift is checked where the environment is — `task plan` for everything,
`task cluster:plan` for the cluster tier, `task e2e` against a running
cluster. The e2e suite could not run in CI regardless: the firewall opens the
Kubernetes API to `network.adminCIDRs` only, and GitHub's published runner
ranges are 6980 rotating CIDRs of shared Azure.

## Why the pipeline is not slow any more

Every run used to pay for a cold compile of a 208-module graph dominated by the
generated Pulumi Kubernetes SDK. Four decisions fixed it, and each one is
commented where it is implemented — with the measurement that chose it, since
that is where somebody about to reverse it will be standing.

**One writer for the shared cache.** `actions/setup-go` derives its key from
`go.sum` and an Actions key is immutable, so whichever job saves first owns it:
early runs failed before they touched Go, saved 31 MB, and every later run
restored those 31 MB and rebuilt everything. `.github/actions/setup-go` takes a
`cache-mode` now — one writer, on a push to `main`, everything else
`restore` — and CI asserts that exactly one writer exists, because a comment
did not prevent the second occurrence.

**The priming job builds unconditionally.** The first version skipped the build
when no Go had changed, saved an empty cache under the immutable key, and
poisoned it on the run that introduced it.

**A job that compiles nothing does not restore the cache.** Restoring it costs
minutes; gitleaks scans for two seconds, trivy for fifteen, checkov is a Python
tool, and lychee is a Rust binary. Even govulncheck, which does compile, saves
26 seconds of work against 191 of restore. They all take `cache-mode: off`, and
`TestWorkflows_RestoreTheGoCacheOnlyWhereSomethingReadsIt` refuses the next job
that pays for a cache nothing reads — five had been fixed one at a time before
it existed.

**The cache is cleaned before it is saved.** A cold build produces 4 635 MB;
the saved one had reached 14 235 MB, because Go trims entries untouched for
five days and a cache restored fresh every run never ages. The priming job runs
`go clean -cache` first, and the key carries a generation — bumped with the
previous one still in `restore-keys`, or the first pull request is cold
everywhere and gosec dies on its bound. On the same job: 429 seconds to 249,
and the stored artifact from 2.0 GB to 1.1.

Two answers worth keeping for whoever changes this again. The build cache does
earn its restore — 332 seconds with it against 713 with the module cache alone.
And `free-disk` is headroom rather than necessity: removing it measures slower
(250 seconds against 209), so it stays, and
[the action itself](../.github/actions/free-disk/action.yml) has the rest.

## What a change does not run

`Changed paths` classifies the diff, and the jobs it gates carry a job-level
`if:`. On a documentation-only pull request they never start a runner.

That is safe because a job skipped by `if:` reports, with a `skipping`
conclusion branch protection accepts as success — `Go build cache` has been
doing exactly that on every pull request, being push-only. What leaves a
required check unreported, and a branch waiting forever, is a workflow-level
`paths:` filter. There is none here.

The filter names what is **inert** — markdown, `docs/`, `LICENSE`,
`.gitignore`, issue templates, images — rather than what is code. It was an
inclusion list once, naming `.go`, `go.mod` and `.golangci.yaml`, and that is
the wrong direction: the test suite asserts against both taskfile entry
points, the per-layer taskfiles, every layer's manifests and `Pulumi.yaml`,
the committed topology and these workflows, so a change to any of them skipped
the tests written to guard it. Excluding documentation cannot fail that way — a kind of
file nobody has classified yet is relevant by default.

Three tests in [internal/ci](../internal/ci) hold it: the classifier against a
table of paths, and both directions of "every gate names an output that
exists". A gate reading an output that does not exist — a rename, a typo —
evaluates to the empty string and skips the job, which reports as skipped,
which branch protection accepts. Nothing else would notice.

### What a prose-only pull request does run

Four jobs, and every one of them reads either the changed files or the pull
request itself:

| Job | Reads |
|-----|-------|
| `Changed paths` | the diff, to answer this question |
| `Commit messages` | the subjects, which every pull request has |
| `Documentation links` | the markdown — this is the gate such a change exists to face |
| `Security / Committed secrets` | the whole history, because a credential can be pasted into a README |

The other four scanners take the same answer as a `code` input and skip:
govulncheck reads the module and the advisory database, trivy reads `go.sum`
and the manifests, zizmor and actionlint read the workflow files. None of those
is something a document can change, so running them re-asserts what the
previous run asserted.

It was seven jobs and 331 seconds, of which 211 were the documentation job
restoring a Go build cache that lychee cannot read.

## What the suites prove

```bash
task -t Taskfile.dev.yaml go:test    # unit
task e2e        # against a running cluster; read-only, needs ./kubeconfig
```

The unit tests pin what Pulumi will *ask for*, including the settings whose
mismatch never fails an apply — kube-proxy replacement, PROXY protocol on both
sides of the load balancer, the KubePrism port. They exercise the resource
graph under Pulumi's mock monitor, so no cloud account is involved.

The e2e suite checks what actually happened: taints cleared, routes
programmed, the load balancer provisioned, volumes bound. It lives behind the
`e2e` build tag, so `go test ./...` never reaches for a cluster. It needs
`./kubeconfig`, which `task cluster:kubeconfig` writes.

It runs from the operator's machine rather than from CI: the firewall opens the
Kubernetes API to `network.adminCIDRs` only, and a GitHub-hosted runner is not
in it.

## Chart upgrades arrive as pull requests

Every chart is pinned in [internal/pkg/charts/registry.go](../internal/pkg/charts/registry.go) —
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
no longer ships. `charts:appversions` reads each repository's index and
fails when they disagree — mostly a misleading comment, but not only a comment
once something derives a value from the field, as a validation image tag was
derived from alloy's while that chart was pinned here. The pull request says so
in its own body, and the fix is the value that check prints.

**The regex is a silent failure waiting to happen.** It keys off the field
order `Name → Repo → Version`; reorder them and Renovate stops matching, opens
no pull request, and reports nothing. So `tools/charts` reads that regex out of
Renovate's own configuration and asserts it still matches every chart in the
registry. Verified by reordering two fields on purpose, when loki was still
pinned here:

```
Renovate's pattern does not match chart "loki" (key "loki") — it would never be upgraded
```
