// Package docs holds the gates that read documentation or .gitignore.
//
// Apart from internal/ci because the paths they read are inert: a prose-only
// pull request skips the build job, so these run in the docs job instead,
// which ignores relevance. That job builds this package without a cache, and
// TestPackage_PullsNoPulumi keeps it cheap enough to.
package docs

import (
	"path/filepath"
	"strings"
)

// repoRoot is the repository root as seen from this package's directory.
var repoRoot = filepath.Join("..", "..", "..")

// relativeToRoot is a path as a reader of the repository would write it.
func relativeToRoot(path string) string {
	return strings.TrimPrefix(filepath.ToSlash(path), filepath.ToSlash(repoRoot)+"/")
}
