package ci

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readmeMinimum is the length below which a README is a placeholder rather
// than a document. The shortest real one here is several times this.
const readmeMinimum = 200

// documentedTrees are the directories whose every child documents itself, and
// which document themselves.
var documentedTrees = []string{"tools", "internal"}

// TestEveryToolDocumentsItself asks each tool and each internal package for a
// README.
//
// A directory named for its subject says what it is about and nothing about
// what it answers, which flag it takes, or who runs it. That was the state
// before these: eleven tools whose only description was a doc comment in
// main.go, so reading one meant opening Go source to find out whether it even
// takes an argument.
//
// The first line must be the directory's own name. A README that opens with
// another tool's heading is a copy left unfinished, and it reads as correct.
func TestEveryToolDocumentsItself(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	var checked int

	for _, tree := range documentedTrees {
		assertREADME(t, root, tree, tree)

		entries, err := os.ReadDir(filepath.Join(root, tree))
		require.NoError(t, err, tree)

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}

			assertREADME(t, root, filepath.Join(tree, entry.Name()), entry.Name())

			checked++
		}
	}

	assert.Positive(t, checked, "no directories found under any documented tree; this test is checking nothing")
}

// assertREADME checks that dir holds a README that is a document and opens
// with the heading name.
func assertREADME(t *testing.T, root, dir, name string) {
	t.Helper()

	path := filepath.Join(dir, "README.md")

	raw, err := os.ReadFile(filepath.Join(root, path))
	if !assert.NoError(t, err, "%s has no README; a directory named for its subject says nothing about what it answers", dir) {
		return
	}

	assert.GreaterOrEqual(t, len(raw), readmeMinimum,
		"%s is %d bytes, which is a placeholder rather than a document", path, len(raw))

	heading := strings.SplitN(string(raw), "\n", 2)[0]
	assert.Equal(t, "# "+name, heading,
		"%s opens with %q instead of its own name, which is what an unfinished copy looks like",
		path, heading)
}

// namedFile matches a file this repository owns, written in backticks the way
// a README names one.
var namedFile = regexp.MustCompile("`([A-Za-z0-9_.-]+\\.(?:go|awk|yaml|yml|json|tmpl))`")

// TestREADMEs_NameFilesThatExist keeps a README from pointing at a file that
// was renamed out from under it.
//
// Both instances found when this was written were the same shape: a document
// describing a test beside it, and the test had since been renamed or folded
// into another file. `tools/lines/README.md` credited `lib_test.go` for
// covering every op, and `internal/ci/README.md` had a row for
// `backupoutputs_test.go`. Neither file was in the tree, and both documents
// read as correct.
//
// Only names in the README's own directory are checked. A path with a
// separator in it is a link, which the documentation link gate already
// resolves.
func TestREADMEs_NameFilesThatExist(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	var checked int

	for _, name := range tracked(t, root) {
		if filepath.Base(name) != "README.md" {
			continue
		}

		raw, readErr := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, readErr, name)

		for _, match := range namedFile.FindAllStringSubmatch(string(raw), -1) {
			beside := filepath.Join(root, filepath.Dir(name), match[1])

			if _, statErr := os.Stat(beside); statErr != nil {
				// A name that is not beside this README may still be a real
				// file somewhere else — Pulumi.yaml, ci.yaml, renovate.json
				// are named by documents that do not sit next to them. Only
				// a name that is nowhere in the tree is a broken reference.
				if anywhere(root, match[1]) {
					continue
				}

				assert.Fail(t, "a README names a file that does not exist",
					"%s names %q, and nothing in the tree is called that",
					name, match[1])
			}

			checked++
		}
	}

	assert.Positive(t, checked, "no README named a file, so this test proved nothing")
}

// anywhere reports whether the tree holds a file with this base name.
func anywhere(root, name string) bool {
	var found bool

	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || found {
			return nil //nolint:nilerr // a directory that cannot be read holds no answer
		}

		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == "node_modules") {
			return filepath.SkipDir
		}

		if !entry.IsDir() && entry.Name() == name {
			found = true
		}

		return nil
	})

	return found
}

// docsLink matches a link in the documentation index, capturing its target.
var docsLink = regexp.MustCompile(`\]\(([^)]+)\)`)

// TestDocsIndex_ListsEveryDocument keeps the index of docs/ complete.
//
// GitHub renders this one when somebody browses the directory, so it is the
// first thing a reader sees there — and a document it omits is a document
// nobody finds from inside the tree.
//
// Read from git rather than from the filesystem, because what is published is
// what is tracked: BACKLOG.md sits in this directory on an operator's machine
// and is gitignored, and an index that demanded a row for it would fail on
// every clone but that one.
func TestDocsIndex_ListsEveryDocument(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	listed, err := exec.CommandContext(t.Context(), "git", "-C", root, "ls-files", "docs").Output()
	require.NoError(t, err)

	index, err := os.ReadFile(filepath.Join(root, "docs", "README.md"))
	require.NoError(t, err)

	targets := map[string]bool{}

	for _, link := range docsLink.FindAllStringSubmatch(string(index), -1) {
		targets[link[1]] = true
	}

	require.NotEmpty(t, targets, "the index links nothing, so this test proved nothing")

	var (
		checked int
		records bool
	)

	for _, path := range strings.Split(strings.TrimSpace(string(listed)), "\n") {
		relative, err := filepath.Rel("docs", path)
		require.NoError(t, err)

		if directory := filepath.Dir(relative); directory != "." {
			// A subdirectory is listed as itself rather than file by file:
			// docs/adr has its own index, and naming six records here would
			// be a second copy of it.
			records = records || targets[filepath.ToSlash(directory)+"/"]

			continue
		}

		if relative == "README.md" || filepath.Ext(relative) != ".md" {
			continue
		}

		checked++

		assert.True(t, targets[relative],
			"docs/README.md does not link %s, so nothing in the directory points at it",
			relative)
	}

	assert.Positive(t, checked, "no document was examined, so this test proved nothing")
	assert.True(t, records, "docs/README.md does not link the adr/ directory")

	for target := range targets {
		if strings.HasPrefix(target, "..") || strings.Contains(target, "://") {
			continue
		}

		_, statErr := os.Stat(filepath.Join(root, "docs", strings.TrimSuffix(target, "/")))
		assert.NoError(t, statErr, "docs/README.md links %q, which is not there", target)
	}
}
