package ci

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// workflow is the part of a GitHub Actions workflow this test walks.
type workflow struct {
	Jobs map[string]struct {
		Steps []struct {
			Name             string `json:"name"`
			Run              string `json:"run"`
			WorkingDirectory string `json:"working-directory"`
		} `json:"steps"`
	} `json:"jobs"`
}

// TestWorkflows_RunGoToolsFromTheWorkspace catches a relative package path
// that resolves against the wrong directory.
//
// `go run ./tools/stack` is correct in a step that runs at the repository
// root and wrong in one that sets working-directory: under
// infra/cluster it resolves to infra/cluster/tools/stack, and Go's answer is
// `stat .../infra/cluster/tools/stack: directory not found`. It failed the
// drift-detection workflow in exactly that way.
//
// Neither actionlint nor a compiler sees it: the path is a string until the
// step runs, and the step only runs on a schedule. The fix is the taskfiles'
// own idiom — address the tool absolutely, from $GITHUB_WORKSPACE.
func TestWorkflows_RunGoToolsFromTheWorkspace(t *testing.T) {
	t.Parallel()

	dir := filepath.Join("..", "..", ".github", "workflows")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	var steps int

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}

		raw, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
		require.NoError(t, readErr)

		var parsed workflow
		require.NoError(t, yaml.Unmarshal(raw, &parsed), entry.Name())

		for job, spec := range parsed.Jobs {
			for _, step := range spec.Steps {
				if step.WorkingDirectory == "" || !strings.Contains(step.Run, "go run ./") {
					continue
				}

				steps++

				assert.Fail(t,
					"a relative go package path in a step that changed directory",
					"%s job %q step %q runs `go run ./…` from %s, where the package path does not resolve — address it as $GITHUB_WORKSPACE/…",
					entry.Name(), job, step.Name, step.WorkingDirectory)
			}
		}
	}

	assert.Zero(t, steps)
}

// gate is the part of a workflow these tests read: what a job is conditional
// on, and what the job it depends on publishes.
type gate struct {
	Jobs map[string]struct {
		If      string            `json:"if"`
		Outputs map[string]string `json:"outputs"`
	} `json:"jobs"`
}

// The two workflows whose gates these tests guard.
const (
	ciWorkflow       = "ci.yaml"
	securityWorkflow = "security.yaml"
)

func readGate(t *testing.T) gate {
	t.Helper()

	return readWorkflowGate(t, ciWorkflow)
}

func readWorkflowGate(t *testing.T, name string) gate {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", name))
	require.NoError(t, err)

	var parsed gate
	require.NoError(t, yaml.Unmarshal(raw, &parsed))

	return parsed
}

// scannersThatAlwaysRun are the jobs in the security workflow with no
// `inputs.code` guard, each with the reason it is exempt.
var scannersThatAlwaysRun = map[string]string{
	"secrets": "a credential can arrive in any kind of file, and gitleaks reads the whole history rather than the diff",
}

// TestSecurityWorkflow_SkipsWhatAProseChangeCannotAffect holds the scope of
// the security workflow to what its scanners actually read.
//
// Four of the five answer from files a prose change does not touch — the
// module and the advisory database, go.sum and the manifests, the workflow
// files twice over — so running them on a documentation-only pull request
// re-asserts what the previous run asserted, for ten minutes. The fifth reads
// every file and is exempt above, by name and with its reason.
//
// The `!= false` form is the load-bearing part. On a schedule or a manual run
// there is no workflow_call, `inputs` is empty, and the input reads as null:
// plain truthiness would skip every scanner on exactly the run this workflow
// exists for, the one where the advisory database moved and the code did not.
func TestSecurityWorkflow_SkipsWhatAProseChangeCannotAffect(t *testing.T) {
	t.Parallel()

	workflow := readWorkflowGate(t, securityWorkflow)
	require.NotEmpty(t, workflow.Jobs, "%s declares no jobs; this test is checking nothing", securityWorkflow)

	for name, job := range workflow.Jobs {
		if reason, exempt := scannersThatAlwaysRun[name]; exempt {
			assert.NotContains(t, job.If, "inputs.code",
				"%s is listed as always running — %s — and carries an inputs.code guard anyway",
				name, reason)

			continue
		}

		assert.Contains(t, job.If, "inputs.code != false",
			"job %q in %s has no `if: inputs.code != false`, so it runs on a change it "+
				"cannot be affected by — or, if it must always run, belongs in "+
				"scannersThatAlwaysRun with the reason", name, securityWorkflow)
	}

	// The caller has to pass the answer, and the input's default is true — so
	// a half-finished rename fails safe by running everything, silently. This
	// is what says it out loud.
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", ciWorkflow))
	require.NoError(t, err)

	assert.Contains(t, string(raw), "code: ${{ needs.changes.outputs.relevant == 'true' }}",
		"%s calls the security workflow without passing `code`, so every scanner falls back "+
			"to the default and a prose change runs all of them", ciWorkflow)
}

// outputReference finds `needs.<job>.outputs.<name>` in a condition.
var outputReference = regexp.MustCompile(`needs\.([A-Za-z0-9_-]+)\.outputs\.([A-Za-z0-9_-]+)`)

// TestCI_EveryGateNamesAnOutputThatExists is the guard for the cheapest and
// worst failure this workflow can have.
//
// A job whose `if:` reads an output that does not exist — a rename, a typo —
// evaluates to the empty string, compares unequal to 'true', and skips. The
// job reports as skipped, branch protection accepts a skipped required check
// as success, and the pull request goes green having run nothing at all.
// Nothing else in this repository would notice.
func TestCI_EveryGateNamesAnOutputThatExists(t *testing.T) {
	t.Parallel()

	workflow := readGate(t)

	var checked int

	for name, job := range workflow.Jobs {
		for _, found := range outputReference.FindAllStringSubmatch(job.If, -1) {
			producer, output := found[1], found[2]

			declared, ok := workflow.Jobs[producer]
			require.True(t, ok, "job %q is gated on job %q, which does not exist", name, producer)

			require.Contains(t, declared.Outputs, output,
				"job %q reads needs.%s.outputs.%s, which %s does not declare — "+
					"the condition would be empty and the job would skip silently",
				name, producer, output, producer)

			checked++
		}
	}

	assert.NotZero(t, checked, "no job is gated on a changed-path output any more")
}

// TestCI_EveryDeclaredOutputIsRead catches the other direction: an output
// nothing consumes is a gate somebody meant to wire up and did not.
func TestCI_EveryDeclaredOutputIsRead(t *testing.T) {
	t.Parallel()

	workflow := readGate(t)

	// The whole file, because a reader is not always a condition: the reusable
	// security workflow takes the same answer as a `with:` input.
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", ciWorkflow))
	require.NoError(t, err)

	for producer, job := range workflow.Jobs {
		for output := range job.Outputs {
			assert.Contains(t, string(raw), "needs."+producer+".outputs."+output,
				"job %q declares output %q that nothing reads", producer, output)
		}
	}
}

// inertPattern lifts the classifier out of the workflow's shell.
//
// Kept to the subset grep -E and Go's regexp read identically — alternation,
// anchors, character classes, escaped dots — so that testing it here is
// testing what CI runs, not an approximation of it.
var inertPattern = regexp.MustCompile(`(?m)^\s*inert='([^']+)'`)

// TestCI_ClassifiesDocumentationAsInert pins the one decision the gate makes.
//
// Both directions are failures, and they are not symmetrical. Calling a
// relevant file inert skips a check that would have failed — silent, green,
// and the reason this list excludes documentation rather than including code:
// the previous inclusion list skipped the test suite for a change to
// Taskfile.yaml or to a layer's manifests, both of which the suite asserts
// against. Calling documentation relevant only wastes eleven minutes.
func TestCI_ClassifiesDocumentationAsInert(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", ciWorkflow))
	require.NoError(t, err)

	found := inertPattern.FindStringSubmatch(string(raw))
	require.NotNil(t, found, "no inert pattern in %s", ciWorkflow)

	pattern, err := regexp.Compile(found[1])
	require.NoError(t, err, "the workflow's pattern does not compile as a regexp")

	for path, inert := range map[string]bool{
		// Documentation, and the files that are only ever read by a person.
		"README.md":                       true,
		"docs/design.md":                  true,
		"docs/img/topology.png":           true,
		"LICENSE":                         true,
		".gitignore":                      true,
		".github/SECURITY.md":             true,
		".github/ISSUE_TEMPLATE/bug.yaml": true,

		// Code, and every other input a check reads. The taskfiles, the
		// manifests, the topology and these workflows are all asserted
		// against by the Go suite, which is why none of them may be inert.
		"internal/pkg/layer/runner.go":                   false,
		"go.mod":                                         false,
		"go.sum":                                         false,
		".golangci.yaml":                                 false,
		"internal/pkg/values/loki.yaml.tmpl":             false,
		"Taskfile.yaml":                                  false,
		"tasks/platform.task.yaml":                       false,
		"layers/20-network-policy/Pulumi.yaml":           false,
		"layers/20-network-policy/manifests/10-dns.yaml": false,
		"infra/cluster/cluster.example.yaml":             false,
		".github/workflows/ci.yaml":                      false,
		".github/actions/setup-go/action.yml":            false,
		".checkov.yaml":                                  false,
		"internal/ci/gitignore_test.go":                  false,
		// A kind of file nobody has classified yet. Relevant by default is
		// what makes forgetting safe.
		"tools/something/new.awk": false,
	} {
		assert.Equal(t, inert, pattern.MatchString(path), "%q", path)
	}
}

// TestCI_DocumentationLinkGateIgnoresRelevance holds the one thing about the
// docs job that a tidy-up would get wrong.
//
// Every other check in ci.yaml is gated on `changes.outputs.relevant`, and
// adding that `&&` here reads like consistency. It would disable the gate in
// the case it exists for. Documentation is INERT by design — see
// TestCI_ClassifiesDocumentationAsInert — so a prose-only change reports
// relevant=false, and a link gate that honours relevance would never run on a
// change to the documents whose links it checks.
//
// The other direction is covered too, and it is the less obvious half: a link
// breaks when the file it points at MOVES, or when a heading is renamed. Those
// are code and documentation changes with nothing broken in their own diff.
// The gate has to run on both, which means on everything.
func TestCI_DocumentationLinkGateIgnoresRelevance(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", ciWorkflow))
	require.NoError(t, err)

	// The job's own `if:`, taken from the block that starts at `  docs:` and
	// ends where the next job begins.
	block := regexp.MustCompile(`(?ms)^  docs:\n(.*?)(?:^  [a-z][a-z0-9-]*:\n)`).
		FindStringSubmatch(string(raw))
	require.Len(t, block, 2, "no docs job in %s", ciWorkflow)

	condition := regexp.MustCompile(`(?m)^\s*if:\s*(.+)$`).FindStringSubmatch(block[1])
	require.Len(t, condition, 2, "the docs job has no if: condition")

	assert.NotContains(t, condition[1], "relevant",
		"the documentation link gate is gated on changed-path relevance. Documentation is "+
			"inert, so that switches the gate off for exactly the changes it checks:\n  if: %s",
		condition[1])

	// And it still has to be a pull-request job rather than running on every
	// push, which is how every other job in this workflow is scoped.
	assert.Contains(t, condition[1], "pull_request",
		"the documentation link gate runs outside a pull request:\n  if: %s", condition[1])
}

// TestDocsLinkTask_ChecksFragmentsOffline pins the two flags that decide what
// the gate is worth.
//
// Neither is cosmetic and neither fails loudly if dropped. Without
// --include-fragments the gate stops checking anchors, and a renamed heading
// leaves every link to it pointing at the top of the page — which is how
// GitHub renders a fragment it cannot find, with no error anywhere. Without
// --offline the gate starts depending on other people's uptime: GitHub's own
// release downloads returned 504 for twenty minutes while this was written,
// which would have failed a run that had nothing to do with them.
func TestDocsLinkTask_ChecksFragmentsOffline(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "Taskfile.yaml"))
	require.NoError(t, err)

	block := regexp.MustCompile(`(?ms)^  docs:links:\n(.*?)(?:^  [a-z][a-z0-9:-]*:\n)`).
		FindStringSubmatch(string(raw))
	require.Len(t, block, 2, "no docs:links task in Taskfile.yaml")

	for _, flag := range []string{"--offline", "--include-fragments"} {
		assert.Contains(t, block[1], flag,
			"the docs:links task no longer passes %s", flag)
	}
}

// commitTypeRule lifts the awk program out of the workflow, so what this test
// exercises is the expression CI runs rather than a paraphrase of it.
var commitTypeRule = regexp.MustCompile(`(?s)awk -v type="\$type" '(.*?)'\)"`)

// TestCI_CommitTypeGateKeepsTasksAndToolsDeployable pins the one boundary the
// gate gets to draw.
//
// The rule refuses feat/fix/perf on a commit that touches only
// contributor-facing paths, because semantic-release reads the subject alone
// and would cut a version for something no consumer receives —
// `fix(ci): let renovate's schedule actually fire` produced v4.4.2 that way.
//
// The boundary has to stay narrow. The taskfiles and tools/ are part of what
// somebody gets by checking out a tag, so a new task IS a feat; putting either
// in the contributor-only list would push real features into `chore` and make
// the release notes worse than no gate at all.
func TestCI_CommitTypeGateKeepsTasksAndToolsDeployable(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", ciWorkflow))
	require.NoError(t, err)

	found := commitTypeRule.FindStringSubmatch(string(raw))
	require.Len(t, found, 2, "no commit-type awk rule in %s", ciWorkflow)

	rule := found[1]

	for _, contributorOnly := range []string{`^\.github\/`, `_test\.go$`} {
		assert.Contains(t, rule, contributorOnly,
			"the gate no longer treats %s as contributor-only, so a release type on it passes",
			contributorOnly)
	}

	// The paths that must NOT be in it, named individually so a failure says
	// which one was added.
	for _, deployable := range []string{"Taskfile", "^tools", "^tasks", "^layers", "^pkg"} {
		assert.NotContains(t, rule, deployable,
			"the gate treats %s as contributor-only; a change there is something a consumer "+
				"of this repository receives, so it may be a feat", deployable)
	}

	// And only the three types semantic-release turns into a version are
	// refused: widening this to every type would make the gate an opinion
	// about vocabulary rather than about releases.
	assert.Contains(t, rule, `type ~ /^(feat|fix|perf)$/`,
		"the gate no longer refuses exactly the release types")
}

// cacheReaders are the ways a job actually reads the shared Go build cache:
// a go command, or a tool that runs the compiler itself. A job restoring the
// cache must match one of these or it is restoring gigabytes for no reader.
var cacheReaders = []string{
	"go test", "go vet", "go run", "go build",
	// golangci-lint compiles every package it lints.
	"golangci/golangci-lint-action",
	// gosec type-checks the tree, through the task an operator runs.
	"task security:gosec",
}

// cacheModes that mean the job is not restoring the shared cache for nothing:
// "off" reads none of it, "save" is the one writer and has to fill it.
var cacheModesWithoutAReader = map[string]bool{"off": true, "save": true}

// TestWorkflows_RestoreTheGoCacheOnlyWhereSomethingReadsIt catches the
// expensive mistake this workflow has made five times.
//
// The shared cache expands 789 MB into 4.4 GB. A job that compiles nothing
// gains nothing from it and pays the restore anyway, and the failure is
// invisible: the job goes green, just slowly, and the whole expensive half of
// CI queues behind it. Each of the five fixed jobs carries the measurement —
// 313 seconds of a 355-second checkov job, 300 for a two-second gitleaks scan,
// 184 for fifteen seconds of trivy.
//
// The sixth was the documentation job, found while scoping CI down for
// prose-only changes: 211 of its 220 seconds, on the one job such a change
// always runs.
func TestWorkflows_RestoreTheGoCacheOnlyWhereSomethingReadsIt(t *testing.T) {
	t.Parallel()

	var checked int

	for _, name := range []string{ciWorkflow, securityWorkflow} {
		raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", name))
		require.NoError(t, err, name)

		var parsed struct {
			Jobs map[string]struct {
				Steps []struct {
					Uses string            `json:"uses"`
					Run  string            `json:"run"`
					With map[string]string `json:"with"`
				} `json:"steps"`
			} `json:"jobs"`
		}

		require.NoError(t, yaml.Unmarshal(raw, &parsed), name)

		for job, declared := range parsed.Jobs {
			var restores bool

			for _, step := range declared.Steps {
				// The composite action, not upstream's actions/setup-go: only
				// the composite one touches the shared cache.
				if !strings.Contains(step.Uses, "./.github/actions/setup-go") {
					continue
				}

				mode := step.With["cache-mode"]
				if mode == "" {
					mode = "restore"
				}

				restores = !cacheModesWithoutAReader[mode]

				break
			}

			if !restores {
				continue
			}

			checked++

			var reads bool

			for _, step := range declared.Steps {
				for _, reader := range cacheReaders {
					if strings.Contains(step.Run, reader) || strings.Contains(step.Uses, reader) {
						reads = true
					}
				}
			}

			assert.True(t, reads,
				"job %q in %s restores the shared Go build cache and no step reads it: "+
					"pass cache-mode \"off\", or add the tool that compiles here to cacheReaders",
				job, name)
		}
	}

	assert.Positive(t, checked, "no job restores the shared cache any more; this test is checking nothing")
}

// privilegedByIssue are the workflows a GitHub issue can start. Each one is
// checked below for a guard on WHO wrote the issue.
var privilegedByIssue = []string{"renovate.yaml"}

// TestWorkflows_AnIssueCannotStartAJobForAStranger is a public-repository
// check written while the repository was still private.
//
// Renovate's Dependency Dashboard is an issue, and a ticked checkbox in it is
// meant to start a run at once rather than at the next cron — so the workflow
// listens for `issues: [edited]` and holds RENOVATE_TOKEN. The guard used to
// be the issue's TITLE, which is enough only while nobody else can open an
// issue: anyone may call theirs `Dependency Dashboard`.
//
// The author is what cannot be forged. Renovate opens the dashboard itself, so
// `github.event.issue.user.login == 'renovate[bot]'` is the exact test, and
// this holds it in place for whoever edits the trigger next.
func TestWorkflows_AnIssueCannotStartAJobForAStranger(t *testing.T) {
	t.Parallel()

	var checked int

	for _, name := range privilegedByIssue {
		raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", name))
		require.NoError(t, err, name)

		text := string(raw)

		// Only workflows an issue can actually start. One that stops
		// listening needs no guard, and should not be failed for dropping it.
		if !strings.Contains(text, "issues:") {
			continue
		}

		checked++

		assert.Contains(t, text, "github.event.issue.user.login == 'renovate[bot]'",
			".github/workflows/%s runs on an issue event without checking who wrote the "+
				"issue, so anyone who can open one can start it", name)
	}

	assert.Positive(t, checked, "no workflow listens for issues; this test is checking nothing")
}
