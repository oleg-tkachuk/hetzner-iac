package ci

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// outputConstant matches one of the backup layer's stack-output constants and
// captures the name it exports.
var outputConstant = regexp.MustCompile(`(?m)^\tOutput\w+\s+= "(\w+)"$`)

// TestBackupOutputs_AreTheNamesTheUploadTaskReads holds the two ends of a
// contract that crosses a language boundary.
//
// layers/60-backup publishes five stack outputs and `task cluster:etcd:upload`
// reads all five with jq. The layer spells them as Go constants; the task
// cannot import one, so it spells them again as taskfile vars. Nothing else
// compares the two, and the failure if they drift is not an error: jq answers
// `null`, the `// ""` default makes it an empty string, and the task's own
// guard reports the layer as unapplied — pointing the operator at an apply
// that will not fix it.
func TestBackupOutputs_AreTheNamesTheUploadTaskReads(t *testing.T) {
	t.Parallel()

	layer, err := os.ReadFile(filepath.Join("..", "..", "layers", "60-backup", "main.go"))
	require.NoError(t, err)

	matches := outputConstant.FindAllStringSubmatch(string(layer), -1)
	require.NotEmpty(t, matches, "the backup layer declares no Output constants; this test is checking nothing")

	tasks, err := os.ReadFile(filepath.Join("..", "..", "tasks", "cluster.task.yaml"))
	require.NoError(t, err)

	body := string(tasks)

	for _, match := range matches {
		name := match[1]

		// strings.Contains behind assert.True rather than assert.Contains:
		// the haystack is a 900-line taskfile, and assert.Contains prints it
		// in full. A failure here is about one word.
		assert.True(t, strings.Contains(body, ": "+name+"\n"),
			"layers/60-backup exports %q and no taskfile var holds that name, "+
				"so cluster:etcd:upload cannot be reading it", name)
	}

	// And the other direction: a var naming an output the layer no longer
	// exports. That one is invisible from the loop above.
	varNames := regexp.MustCompile(`(?m)^  _CL_BACKUP_OUT_\w+: (\w+)$`).FindAllStringSubmatch(body, -1)
	require.Len(t, varNames, len(matches),
		"the upload task reads a different number of outputs than the layer exports")

	exported := map[string]bool{}
	for _, match := range matches {
		exported[match[1]] = true
	}

	for _, match := range varNames {
		assert.True(t, exported[match[1]],
			"cluster:etcd:upload reads %q, which layers/60-backup does not export", match[1])
	}
}
