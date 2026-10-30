package docs

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/ci/citest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPackage_PullsNoPulumi keeps this package cheap to build.
//
// It is the one Go the docs job compiles, with no build cache, on every pull
// request. Pulumi's SDK is 768 packages; one import of a component package
// would turn a few seconds into minutes on the job a prose change always runs.
//
// Through `go list -test` rather than the parser walk in internal/ci: every
// file here is a test, and that walk skips tests by design.
func TestPackage_PullsNoPulumi(t *testing.T) {
	t.Parallel()

	out, err := exec.CommandContext(t.Context(), "go", "list", "-deps", "-test", "-json=ImportPath", ".").Output()
	require.NoError(t, err)

	decoder := json.NewDecoder(bytes.NewReader(out))

	var listed int

	for {
		var pkg struct{ ImportPath string }

		decodeErr := decoder.Decode(&pkg)
		if errors.Is(decodeErr, io.EOF) {
			break
		}

		require.NoError(t, decodeErr)

		listed++

		assert.False(t, strings.HasPrefix(pkg.ImportPath, citest.PulumiPrefix),
			"this package reaches %s. The docs job builds it uncached on every pull request; "+
				"read the file the check needs instead of importing the package that declares it",
			pkg.ImportPath)
	}

	assert.Positive(t, listed, "go list named no packages, so this proved nothing")
}
