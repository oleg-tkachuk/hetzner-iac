package ci

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// commitTypeRule is the one copy of the rule, which both the workflow and the
// commit-msg hook run.
var commitTypeRule = filepath.Join("..", "..", ".github", "commit-type.awk")

// wanted runs the rule over paths and returns the type it says to use, empty
// when the type given is acceptable.
func wanted(t *testing.T, commitType string, paths ...string) string {
	t.Helper()

	command := exec.CommandContext(t.Context(),
		"awk", "-v", "type="+commitType, "-f", commitTypeRule)
	command.Stdin = strings.NewReader(strings.Join(paths, "\n") + "\n")

	var out bytes.Buffer

	command.Stdout = &out

	require.NoError(t, command.Run())

	return strings.TrimSpace(out.String())
}

// TestCommitTypeRule_AnswersForEachKindOfChange pins the rule that decides
// whether a commit may be typed in a way that cuts a release.
//
// It is tested here rather than trusted because two things run it — the
// workflow, per commit of a pull request, and the commit-msg hook, over what
// is staged — and because being wrong in either direction costs something:
// refusing a legitimate `feat` blocks a commit, and letting prose through as
// `fix` publishes a version nobody receives.
func TestCommitTypeRule_AnswersForEachKindOfChange(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		commitType string
		paths      []string
		want       string
	}{
		"prose as docs is what docs is for": {
			commitType: "docs", paths: []string{"README.md", "docs/ci.md"}, want: "",
		},
		"prose as chore still has to be docs": {
			commitType: "chore", paths: []string{"README.md"}, want: "docs",
		},
		"prose as fix above all": {
			commitType: "fix", paths: []string{"docs/design.md"}, want: "docs",
		},
		"prose and a gate are not a fix": {
			commitType: "fix",
			paths:      []string{"README.md", "internal/ci/domains_test.go"},
			want:       "ci, test or chore",
		},
		"prose and a gate as chore is fine": {
			commitType: "chore",
			paths:      []string{"README.md", "internal/ci/domains_test.go"},
			want:       "",
		},
		"a workflow and a dotfile config are contributor-only": {
			commitType: "perf",
			paths:      []string{".github/workflows/ci.yaml", ".golangci.yaml"},
			want:       "ci, test or chore",
		},
		"a task is a real feat, because a tag carries it": {
			commitType: "feat", paths: []string{"tasks/cluster.task.yaml"}, want: "",
		},
		"a tool is a real feat for the same reason": {
			commitType: "feat", paths: []string{"tools/stack/main.go"}, want: "",
		},
		"code beside prose is still code": {
			commitType: "fix",
			paths:      []string{"README.md", "internal/pkg/hetzner/config.go"},
			want:       "",
		},
		"a layer's manifest is not contributor-only": {
			commitType: "fix",
			paths:      []string{"layers/20-network-policy/manifests/20-allow-apiserver.yaml"},
			want:       "",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, test.want, wanted(t, test.commitType, test.paths...))
		})
	}
}

// TestCommitTypeRule_SaysNothingAboutNoPaths keeps an empty commit from being
// refused: a merge or a revert reaches the workflow with no paths of its own,
// and the callers skip those by subject — this is the second line of that.
func TestCommitTypeRule_SaysNothingAboutNoPaths(t *testing.T) {
	t.Parallel()

	assert.Empty(t, wanted(t, "fix"))
	assert.Empty(t, wanted(t, "feat", ""))
}

// TestCommitTypeRule_KeepsTasksAndToolsDeployable pins the one boundary the
// rule gets to draw.
//
// It has to stay narrow. The taskfiles and tools/ are part of what somebody
// gets by checking out a tag, so a new task IS a feat; putting either in the
// contributor-only list would push real features into `chore` and make the
// release notes worse than no rule at all.
//
// Read as text rather than exercised, because what this checks is what the
// list contains — a behaviour test covers a path it was given, and this covers
// the ones nobody thought to give it.
func TestCommitTypeRule_KeepsTasksAndToolsDeployable(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(commitTypeRule)
	require.NoError(t, err)

	rule := string(raw)

	for _, contributorOnly := range []string{`^\.github\/`, `_test\.go$`} {
		assert.Contains(t, rule, contributorOnly,
			"the rule no longer treats %s as contributor-only, so a release type on it passes",
			contributorOnly)
	}

	// The paths that must NOT be in it, named individually so a failure says
	// which one was added.
	for _, deployable := range []string{"Taskfile", "^tools", "^tasks", "^layers", "^internal/pkg"} {
		assert.NotContains(t, rule, deployable,
			"the rule treats %s as contributor-only; a change there is something a consumer "+
				"of this repository receives, so it may be a feat", deployable)
	}

	// And only the three types semantic-release turns into a version are
	// refused: widening this to every type would make it an opinion about
	// vocabulary rather than about releases.
	assert.Contains(t, rule, `type ~ /^(feat|fix|perf)$/`,
		"the rule no longer refuses exactly the release types")
}
