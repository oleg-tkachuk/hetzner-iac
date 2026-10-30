package ci

// The library tasks this repository refuses stay excluded. What the documents
// say about tasks is held in internal/ci/docs.

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/ci/citest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// libraryExclude is one library task this repository refuses, and where.
type libraryExclude struct {
	entryPoint string
	include    string
	task       string
	because    string
}

// libraryExcludes are the exclusions whose failure is silent.
//
// `excludes` naming a task the module does not have removes nothing, and says
// nothing: the task list simply grows by one. The helm entry is the measured
// case — the task was `uninstall-all` until taskfiles v6.0.0 renamed it, so
// the bump that carried the rename would have quietly handed this repository a
// helm uninstall it must not have.
var libraryExcludes = []libraryExclude{
	{
		entryPoint: "Taskfile.yaml",
		include:    "helm",
		task:       "uninstall",
		because: "every Helm release on this cluster is created by Pulumi. Uninstalling one " +
			"with Helm leaves Pulumi state claiming it exists, so the next `up` reports no " +
			"changes while the cluster is empty",
	},
	{
		entryPoint: citest.DevTaskfile,
		include:    "checkov",
		task:       "baseline",
		because: "a baseline accepts every current finding at once with nothing saying why " +
			"any of them stands, and this repository accepts findings the other way — " +
			"skip-check in .checkov.yaml, each with its reason beside it",
	},
}

// TestLibraryExcludes_StayInPlace holds each of them.
func TestLibraryExcludes_StayInPlace(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	for _, excluded := range libraryExcludes {
		t.Run(excluded.include+":"+excluded.task, func(t *testing.T) {
			t.Parallel()

			raw, err := os.ReadFile(filepath.Join(root, excluded.entryPoint))
			require.NoError(t, err, excluded.entryPoint)

			// The include block, so the assertion is about that module rather
			// than about the word appearing anywhere in the file.
			block := regexp.MustCompile(`(?ms)^  ` + excluded.include + `:\n(.*?)(?:^  [a-z#])`).
				FindStringSubmatch(string(raw))
			require.Len(t, block, 2, "no %s include in %s", excluded.include, excluded.entryPoint)

			// Anchored to the end of the line, not `\b`: a word boundary sits
			// between `uninstall` and the hyphen in `uninstall-all`, so the
			// obvious pattern matches the stale name it exists to reject.
			// Caught by trying to make this test fail.
			assert.Regexp(t, `(?m)excludes:[\s\S]*?- `+excluded.task+` *$`, block[1],
				"the %s include no longer excludes %s, or excludes a name the module does "+
					"not have — %s", excluded.include, excluded.task, excluded.because)
		})
	}
}
