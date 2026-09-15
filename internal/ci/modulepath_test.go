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

var (
	// modulePath is the first line of go.mod.
	modulePath = regexp.MustCompile(`(?m)^module\s+(\S+)$`)

	// majorSuffix is the /vN a module path needs to be importable at v2+.
	majorSuffix = regexp.MustCompile(`/v[2-9][0-9]*$`)
)

// notALibrary is the claim the README has to carry while the path has no
// suffix. Matched on the heading rather than a sentence, so the prose under it
// can be rewritten without failing this.
const notALibrary = "### Not a Go library"

// TestModulePath_SaysSoWhenNothingCanImportIt pairs go.mod with the README.
//
// Go requires a `/vN` suffix on the module path of anything released at v2 or
// above. This module has none and its tags are well past v1, so `go get`
// resolves no version of it and pkg.go.dev has nothing to show — which reads
// as a broken module rather than as a deliberate one, and will read that way
// to strangers once the repository is public.
//
// Nothing here is importable in any case: everything outside internal/ is a
// main package, and Go refuses an import of internal/ from another module. So
// the fix is not the suffix, it is saying so. This test makes sure the saying
// survives, and stops asking the moment the path grows a suffix.
func TestModulePath_SaysSoWhenNothingCanImportIt(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	require.NoError(t, err)

	found := modulePath.FindStringSubmatch(string(gomod))
	require.NotNil(t, found, "go.mod declares no module path")

	path := found[1]

	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	require.NoError(t, err)

	if majorSuffix.MatchString(path) {
		// It grew a suffix, so it is importable and the note is stale rather
		// than required. Not asserted away — removing it is a decision.
		t.Logf("module path %s carries a major suffix; the README note is no longer required", path)

		return
	}

	assert.True(t, strings.Contains(string(readme), notALibrary),
		"the module path %s has no /vN suffix, so no released version of it resolves for "+
			"`go get`. The README has to say so under %q, or somebody will report it as a bug",
		path, notALibrary)
}
