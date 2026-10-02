# How changes land

Everything goes through a pull request; `main` is protected and takes no direct
pushes.

```
branch → PR → CI → rebase merge → release
```

- **Rebase is the only merge method.** Each commit of the pull request lands on
  `main` as it was written.
- **Every commit is checked against Conventional Commits.** Under rebase they
  all land on `main`, and semantic-release reads each of them to pick the next
  version and write the notes. A commit that does not conform contributes
  nothing, and a pull request made entirely of them produces no release. The
  `Commit messages` job uses the same expression as the local commit-msg hook;
  the hook is opt-in per clone, the job is a required check.
- **`main` requires linear history** and refuses force pushes, so the commit a
  release points at is the commit that was tested.
- **Release is [`release.yaml`](../.github/workflows/release.yaml)**, dispatched
  by CI once every check on `main` has passed.

Release notes are generated from the commit history by semantic-release; there
is no changelog file to keep in step.

## How a release is cut

The `Release` job in `ci.yaml` carries `needs: [prime, security, build]` and
runs only on a push to `main`, so a red pipeline never reaches it. Its one step
sends a `repository_dispatch` of type `release`, and
[`release.yaml`](../.github/workflows/release.yaml) runs semantic-release as a
run of its own, one at a time.

`release.yaml` has **no** `workflow_dispatch`: a button would release from an
untested `main`. Two simpler-looking triggers do not work here — `workflow_run`
is flagged by zizmor's `dangerous-triggers` audit, and a tag pushed by CI with
`GITHUB_TOKEN` starts no workflow, so it would need a long-lived token.
`repository_dispatch` is one of the documented exceptions that starts a run
from `GITHUB_TOKEN`.

## What runs when

| Job | Runs on | Protects against |
|---|---|---|
| `Changed paths` | every event | — classifies the diff for the gates below |
| `Go build cache` | push to `main` | a pull request compiling from cold; the only job that writes the cache |
| `Tests and vet` | pull request and push to `main` | a failing test, unformatted code, a vet finding — on `main`, two pull requests green apart and broken together |
| `Layer list matches the tree` | pull request | a layer the layer list and the tree disagree on |
| `Documentation links` | pull request | a link to a file or heading that does not exist |
| `Go lint` | pull request | golangci-lint findings, at the pinned version |
| `Taskfile shell` | pull request | shellcheck findings in the shell inside the taskfiles |
| `Insecure patterns` | pull request | gosec findings across the whole module, including ones already on `main` |
| `Manifest hardening` | pull request | checkov's hardening rules on the manifests and the workflows |
| `Chart pins` | pull request | a chart that does not render as declared, or an `AppVersion` that disagrees with its chart |
| `Cluster topology` | pull request | a committed topology that does not parse, or Talos patches that do not validate |
| `Commit messages` | pull request | a subject that is not a Conventional Commit |
| `Security / …` | every event, and weekly | see [Security scanning](#security-scanning) |
| `Release` | push to `main` | — dispatches the release once everything above is green |
| `Race detector` | nightly | data races; too slow for every pull request |
| `Vendored files still match upstream` | nightly | a vendored manifest that upstream no longer serves as recorded |

**Nothing waits for the scanners.** They are required checks, so a red scanner
still blocks the merge; only `Release` lists `security` in its `needs:`.

`Tests and vet` runs on `main` because branch protection does not require a
branch to be current before it merges. The other checks stay
pull-request-only because they read one tree rather than a combination of
merged ones.

Nothing is previewed or applied in CI, and no workflow holds a Pulumi token.
Drift is checked where the environment is — `task plan` for everything,
`task cluster:plan` for the cluster tier, `task e2e` against a running
cluster.

## What a change does not run

`Changed paths` classifies the diff, and the jobs it gates carry a job-level
`if:`. A job skipped by `if:` reports with a `skipping` conclusion, which branch
protection accepts as success. A workflow-level `paths:` filter would leave a
required check unreported and the branch waiting forever, so there is none.

The filter names what is **inert** — markdown, `docs/`, `LICENSE`,
`.gitignore`, issue and pull-request templates, images — rather than what is
code, so a kind of file nobody has classified yet is relevant by default.

Three tests in [internal/ci](../internal/ci) hold it: the classifier against a
table of paths, and both directions of "every gate names an output that
exists". A gate reading an output that does not exist evaluates to the empty
string and skips the job, which branch protection accepts.

### What a prose-only pull request does run

| Job | Reads |
|-----|-------|
| `Changed paths` | the diff, to answer this question |
| `Commit messages` | the subjects, which every pull request has |
| `Documentation links` | the markdown — this is the gate such a change exists to face |
| `Security / Committed secrets` | the whole history, because a credential can be pasted into a README |

The other security scanners take the same answer as a `code` input and skip:
none of what they read is something a document can change.

## Security scanning

The scanners live in [`security.yaml`](../.github/workflows/security.yaml),
which CI calls and which also runs weekly on its own, so a CVE published after
a commit went green still turns it red.

| Job | Tool | Reads |
|---|---|---|
| `Committed secrets` | gitleaks | the whole history |
| `Reachable vulnerabilities` | govulncheck | the module, reporting only what this code can reach |
| `Dependencies and filesystem` | trivy | `go.sum` and the manifests, reporting everything present |
| `Workflow permissions` | zizmor | the workflows, for security |
| `Workflow syntax` | actionlint | the workflows, for correctness, with shellcheck over every `run:` block |

govulncheck and trivy disagree by design; a finding in one and not the other is
information rather than a contradiction.

Each job installs its tool and then calls the same task an operator runs
locally, so the flags live in one place. `task -t Taskfile.dev.yaml scan` is
the whole set, checkov included.

Accepted findings live in `.trivyignore.yaml` and `.checkov.yaml`, each with
the reason it stands. Remove an entry as soon as a fix lands — a stale ignore
masks the finding coming back.

## The entry point the jobs call

Every step that runs a task passes `-t Taskfile.dev.yaml`:

```
- run: task -t Taskfile.dev.yaml security:gosec
```

The checks live on a second entry point so that `task` on its own lists only
what an operator runs. Forgetting the flag resolves the name against the root
entry point, which either fails with "does not exist" or runs the wrong task.
TestWorkflows_CallTasksThroughTheDevTaskfile refuses a step without it, and
TestRootTaskfile_HoldsNoCheck refuses a check added back to the root.

## The build cache

`Go build cache` is the one writer: it runs on a push to `main`, cleans the
cache, builds unconditionally and saves it. Every other job restores the last
cache from `main` or takes `cache-mode: off` when it compiles nothing. CI
asserts that exactly one job saves, and
`TestWorkflows_RestoreTheGoCacheOnlyWhereSomethingReadsIt` refuses a job that
restores a cache nothing in it reads.

The key carries a generation (`go2-`). Bump it with the previous one still in
`restore-keys` for the transition, or the first pull request after the bump is
cold everywhere.

A pull request that changes `go.sum` is the slow case: the exact key misses,
the prefix restores the previous dependencies, and anything that type-checks
the module compiles the difference. That is why `Insecure patterns` has a
25-minute bound and prints the runner's cores and memory before it starts.

gosec is downloaded and verified against its release's `checksums.txt`, not
built with `go install`. `GOSEC_FLAGS` sets its concurrency from the cores the
machine has.

[`free-disk`](../.github/actions/free-disk/action.yml) clears space only when
the runner has less free than it needs.

## Renovate runs when you ask it to

Renovate runs from
[.github/workflows/renovate.yaml](../.github/workflows/renovate.yaml), not as
the hosted app. Two schedules decide what happens:

- the workflow's cron, `0 6 * * *`, decides how often it **runs** — daily;
- `schedule` in `renovate.json`, `* * * * 1` in `Europe/Kyiv`, decides what it
  may **do** — open pull requests on Mondays, so updates batch into one weekly
  review.

The schedule is the whole of Monday because GitHub runs scheduled workflows
best-effort and can start them hours late. A test in `internal/ci` refuses an
hour-narrow schedule.

`workflow_dispatch` runs a pass now and sets `RENOVATE_FORCE` to ignore the
Monday schedule (`ignoreSchedule`, on by default), with `dryRun` and `logLevel`
inputs.

Every Renovate pull request auto-merges by rebase once the required checks
pass, so branch protection is what holds a bad upgrade back. At most five are
open at once and two are opened per hour.

| Update | Commit type | Grouping |
|---|---|---|
| chart, minor or patch | `fix(charts)` | one pull request per chart |
| chart, major | `feat(charts)` | one pull request per chart |
| Go modules | `chore(deps)` | `k8s.io/**` and `sigs.k8s.io/**` together; other minor and patch moves together |
| GitHub Actions | `chore(deps)` | one group, pinned to commit digests |
| pinned CI tools | `chore(ci)` | one group |
| shared task library | `chore(tasklib)` | — |
| `pulumi-kit` | `chore(pulumi-kit)` | — |
| runner image | `ci(runner)` | — |

### Its token, and why vulnerability alerts are off

`RENOVATE_TOKEN` is a personal access token, not `GITHUB_TOKEN`: a pull request
opened with the latter starts no `pull_request` workflow, so no required check
would ever report and branch protection would block every upgrade. The workflow
states the permissions it needs and refuses to start without the secret.

`vulnerabilityAlerts` is **off**. Reading those alerts needs `Dependabot
alerts: Read-only` on a fine-grained token — or the `security_events` scope on
a classic one — which this token does not have. What still reports a
vulnerability: Dependabot alerts on the repository itself, govulncheck in
*Reachable vulnerabilities*, and trivy over `go.sum` and the manifests. A
security fix does not override the schedule; it arrives with Monday's batch.

Turning it back on is one change, not two: grant the permission and flip the
setting together. `TestRenovateAlerts_AgreeWithWhatTheTokenIsToldToAllow`
refuses either half alone.

### A ticked checkbox acts at once

Ticking a box on the Dependency Dashboard records a request that Renovate
fulfils on its next run, so the workflow also triggers on `issues: edited`,
filtered to the dashboard.

Renovate rewrites the dashboard on every pass with the same token, and events
from a personal access token start workflow runs, so its own edit fires the
trigger. A guard step proceeds only when a checkbox carrying a Renovate marker
went from unchecked to checked; Renovate un-ticks after acting, so its own
rewrite never adds one.

### Pinned tool versions

Every tool version in `.github/workflows/*.yaml` is pinned and carries the
annotation a custom manager reads:

```yaml
# renovate: datasource=github-releases depName=securego/gosec
GOSEC_VERSION: "v2.29.0"
```

The annotation holds the datasource — GitHub releases, PyPI or Go modules — so
the pattern knows no special cases. `extractVersion` appears where the pin
omits the `v` (or other prefix) its upstream tag carries; without it Renovate
finds nothing to compare and opens no pull request.

Tests in `internal/ci` run Renovate's own regex out of its configuration and
assert that every pin is matched and every pin is annotated, because a pin
Renovate cannot match fails silently: no pull request, ever.

A second custom manager watches the two pins in the task entry points: the
shared task library's ref, which is part of a URL, and `PULUMI_KIT_VERSION`,
which `go run` fetches. `respectLatest` is off for `pulumi-kit`, because the Go
module proxy's `@latest` lags its version list and would hold Renovate one
release behind.

A task library bump invalidates every checksum under `.task/remote`, because
Task names its trust files for the sha256 of the whole URL. The post-upgrade
task `bash .github/renovate/trust-tasklib.sh` replaces them on the same branch.
Every post-upgrade command must also be listed in `RENOVATE_ALLOWED_COMMANDS`
in `renovate.yaml`, or Renovate skips it with a warning.

The runners are pinned to `ubuntu-24.04` rather than `ubuntu-latest`, and
Renovate's built-in `github-runners` datasource proposes the next image as a
`ci(runner)` pull request. `.github/actionlint.yaml` declares the newer label so
that pull request is reviewable rather than red. `TestWorkflows_PinTheRunner`
keeps a floating label from returning.

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
| `gosec` | `security:gosec` |
| `trivy` | `security:trivy` |
| `checkov` | `task -t Taskfile.dev.yaml checkov:scan`, and `pipx` when checkov itself is not installed — the shared module runs the pinned version through it, reading the version out of this workflow |
| `shellcheck` | `task -t Taskfile.dev.yaml shell` — the shell inside the taskfiles, through `tools/taskshell` |
| `actionlint` | workflow syntax — run by hand, the same check CI runs |
| `zizmor` | workflow permissions — the same |
| `lefthook` | the commit and push hooks, opt in per clone with `lefthook install` |

Each task states what to install rather than skipping itself silently: a gate
that skips itself when a tool is absent is a gate that quietly stops running.

Two linters read shell, and they read different files: actionlint runs
shellcheck over every `run:` block in `.github/workflows/`, and
`tools/taskshell` extracts the shell out of the taskfiles, resolves the template
actions as far as the taskfile itself resolves them, and maps shellcheck's
findings back to the taskfile line. A `# shellcheck disable=` directive takes its
reason after a `#`, not after `--`; the latter is a parse error that disables
nothing.

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
per chart on the Monday schedule, labelled `charts`, with `fix(charts):` so the
upgrade reaches a release (`feat(charts):` for a major, so the version says so).

**A chart that moves an unsigned image's default tag** leaves its digest pin in
`internal/pkg/imagepolicy/images.yaml` behind. The post-upgrade task
`go run ./tools/charts repin` moves it on the same branch.

**Renovate cannot maintain `AppVersion`.** The helm datasource knows chart
versions and nothing else, so `Chart pins` fails the pull request until a human
sets it. The pull request says so in its own body; run `task -t Taskfile.dev.yaml charts:appversions`
for the value to set, and update the `// app <version>` comment beside
`Version` to match.

**The regex keys off the field order `Name → Repo → Version`.** Reorder them and
Renovate stops matching, opens no pull request, and reports nothing. So
`tools/charts` reads that regex out of Renovate's own configuration and asserts
it still matches every chart in the registry, failing with:

```
Renovate's pattern does not match chart "<name>" (key "<key>") — it would never be upgraded
```
