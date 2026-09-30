package ci

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errorExit matches the line a tool prints before exiting non-zero, in either
// of the two spellings this repository has used.
var errorExit = regexp.MustCompile(`fmt\.(Fprintf|Fprintln)\(os\.Stderr, "error:`)

// wantedExit is the one it should be.
const wantedExit = `fmt.Fprintf(os.Stderr, "error: %v\n", err)`

// failureExit is the exit code every program reports a failure with.
const failureExit = "1"

// logImport is the standard logger, whose Fatal and Panic families print a
// failure their own way and exit behind the program's back.
const logImport = "log"

// osImport is where os.Exit lives.
const osImport = "os"

// TestPrograms_ReportAFailureTheSameWay keeps every main() printing one line.
//
// The output was already identical — Fprintln with two arguments and Fprintf
// with %v produce the same bytes — so this is not about what an operator sees.
// It is about the next program: two spellings means the next author picks one
// at random, and then a third, and the taskfiles that grep for "error:" have
// nothing to rely on.
//
// Every main package, not just tools/. Reading only tools/*/main.go is how
// policy/main.go kept a `panic(err)` — a Go stack trace over the one sentence
// that matters, and exit 2 where everything else exits 1. A gate that misses
// a sibling directory is the third of that shape found in this repository.
//
// Judging only the lines already spelt `"error:` was the second hole: it
// checked the spelling of the lines that had it and never asked whether a
// program had one at all. `log.Fatal(err)`, `fmt.Fprintln(os.Stderr, err)`
// before os.Exit(1), and a bare os.Exit(1) all passed. So the rule is now per
// package: no log.Fatal or log.Panic anywhere, and a package that exits 1 has
// the wanted line for its errors. One path may still exit 1 without it —
// tools/orphans does when it finds an orphan, which its report has said — but
// a package cannot exit 1 with no such line at all.
func TestPrograms_ReportAFailureTheSameWay(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	mains, err := mainPackages(t, root)
	require.NoError(t, err)
	require.NotEmpty(t, mains, "no main package found; this test is checking nothing")

	var checked int

	exitsOne := map[string]bool{}
	reports := map[string]bool{}

	for _, path := range mains {
		raw, readErr := os.ReadFile(path)
		require.NoError(t, readErr, path)

		body := string(raw)
		dir := relativeToRoot(filepath.Dir(path))

		assert.NotContains(t, body, "panic(err)",
			"%s reports a failure by panicking: a stack trace instead of a sentence, and "+
				"exit 2 where every other program exits 1", relativeToRoot(path))

		for _, line := range strings.Split(body, "\n") {
			if !errorExit.MatchString(line) {
				continue
			}

			checked++

			assert.Equal(t, wantedExit, strings.TrimSpace(line),
				"%s prints a failure in a second spelling", relativeToRoot(path))

			reports[dir] = true
		}

		file, parseErr := parser.ParseFile(token.NewFileSet(), path, raw, parser.SkipObjectResolution)
		require.NoError(t, parseErr, path)

		ast.Inspect(file, func(node ast.Node) bool {
			call, isCall := node.(*ast.CallExpr)
			if !isCall {
				return true
			}

			pkg, name := calledFrom(file, call)

			switch {
			case pkg == logImport && (strings.HasPrefix(name, "Fatal") || strings.HasPrefix(name, "Panic")):
				assert.Fail(t, "a failure reported by the log package",
					"%s calls log.%s, which prints its own way and exits without %s",
					relativeToRoot(path), name, wantedExit)
			case pkg == osImport && name == "Exit" && len(call.Args) == 1:
				if literal, isLiteral := call.Args[0].(*ast.BasicLit); isLiteral && literal.Value == failureExit {
					exitsOne[dir] = true
				}
			}

			return true
		})
	}

	require.NotEmpty(t, exitsOne, "no program exits %s; the call pattern has drifted", failureExit)

	for dir := range exitsOne {
		assert.True(t, reports[dir],
			"%s exits %s and never prints %s, so its failure is reported some other way",
			dir, failureExit, wantedExit)
	}

	assert.Positive(t, checked, "no program prints a failure; this test is checking nothing")
}

// calledFrom names the import path and function a call goes to, when it is a
// package-qualified call: `log.Fatal(err)` is ("log", "Fatal"). The path comes
// from the file's imports rather than the identifier, so an aliased import is
// still recognised.
func calledFrom(file *ast.File, call *ast.CallExpr) (pkg, name string) {
	selector, isSelector := call.Fun.(*ast.SelectorExpr)
	if !isSelector {
		return "", ""
	}

	qualifier, isIdent := selector.X.(*ast.Ident)
	if !isIdent {
		return "", ""
	}

	for _, spec := range file.Imports {
		imported := strings.Trim(spec.Path.Value, `"`)

		local := imported[strings.LastIndex(imported, "/")+1:]
		if spec.Name != nil {
			local = spec.Name.Name
		}

		if local == qualifier.Name {
			return imported, selector.Sel.Name
		}
	}

	return "", ""
}

// mainPackages finds every main.go in a package clause of `main`, anywhere in
// the tree — tools, policy, the layers and the cluster tier alike.
func mainPackages(t *testing.T, root string) ([]string, error) {
	t.Helper()

	var found []string

	// Tracked files only: a main package that exists only in somebody's
	// working directory is not a program this repository ships.
	for _, name := range tracked(t, root) {
		// Any Go file in a main package, not main.go alone: a program that
		// splits its entry point from its wiring — layers/10-node-platform
		// does — would otherwise be able to move the failure line into a file
		// this never opens.
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		path := filepath.Join(root, name)

		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, readErr
		}

		if strings.HasPrefix(string(raw), "package main") ||
			strings.Contains(string(raw), "\npackage main\n") {
			found = append(found, path)
		}
	}

	return found, nil
}
