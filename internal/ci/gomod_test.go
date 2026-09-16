package ci

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requirement matches one line of a go.mod require block: the module path and
// its version.
var requirement = regexp.MustCompile(`(?m)^\s+(\S+)\s+(v\S+)`)

// majorSuffixed matches the /vN a module path carries from v2 onward.
var majorSuffixed = regexp.MustCompile(`^(.*)/v([2-9][0-9]*)$`)

// sharedTypes are the libraries whose types cross module boundaries, so every
// requirer has to agree on ONE major of them.
//
// Not a general rule against two majors: this module holds three such pairs
// happily — github.com/blang/semver at 1 and 4, go.yaml.in/yaml at 2 and 3,
// github.com/pgavlin/fx at 1 and 2 — because nothing passes those libraries'
// types between the two. Written as a general rule first, this test reported
// all three, which is a gate nobody would keep.
var sharedTypes = map[string]string{
	"sigs.k8s.io/structured-merge-diff": "k8s.io/apimachinery hands a k8s.io/kube-openapi schema " +
		"straight to it, so the three have to agree",
}

// TestGoMod_SharedTypeLibrariesStayAtOneMajor turns a compile error deep in
// somebody else's package into a sentence.
//
// `go get -u ./...` moved k8s.io/kube-openapi to a master pseudo-version
// requiring structured-merge-diff/v7 while the released k8s.io/apimachinery
// still used /v6 — and apimachinery converts a kube-openapi schema with it.
// The build failed like this:
//
//	apimachinery/pkg/util/managedfields/internal/typeconverter.go:51:61:
//	cannot use typeSchema.Types (…/v7/schema.TypeDef) as …/v6/schema.TypeDef
//
// Which names neither the upgrade that caused it nor the reason the two cannot
// mix. kube-openapi has no releases at all: its master runs ahead of the
// Kubernetes release train, so a blanket upgrade always takes it past whatever
// apimachinery was built against.
func TestGoMod_SharedTypeLibrariesStayAtOneMajor(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	require.NoError(t, err)

	majors := map[string][]string{}

	for _, match := range requirement.FindAllStringSubmatch(string(raw), -1) {
		path := match[1]

		base, major := path, "1"
		if suffixed := majorSuffixed.FindStringSubmatch(path); suffixed != nil {
			base, major = suffixed[1], suffixed[2]
		}

		if _, watched := sharedTypes[base]; watched {
			majors[base] = append(majors[base], major)
		}
	}

	require.NotEmpty(t, majors,
		"go.mod requires none of the watched libraries; either they are gone or the "+
			"require block stopped parsing, and both mean this test checks nothing")

	for base, found := range majors {
		sort.Strings(found)

		assert.Len(t, found, 1,
			"go.mod requires %s at majors %s, and %s. Upgrade the direct requirements and "+
				"let the resolver pick the rest: `go get -u ./...` walks indirect modules too, "+
				"and one of them has no releases to stay behind",
			base, strings.Join(found, ", "), sharedTypes[base])
	}
}
