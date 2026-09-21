package ci

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// appendOntoOptions matches an append whose first argument is a resource
// option slice somebody else owns.
//
// Deliberately narrow — the receivers this repository actually threads
// options through — rather than every `append(` with a plausible name. A gate
// that fires on unrelated code is a gate somebody adds an exception to.
var appendOntoOptions = regexp.MustCompile(`append\((opts|options|r\.Options|base)\s*,`)

// TestNoAppendOntoASharedOptionSlice keeps one trap out of the tree.
//
// `append(opts, pulumi.DependsOn(x))` reads like building a child's options
// and is not: when opts has spare capacity it writes into the CALLER's array,
// so two such lines share a slot and the option added second replaces the
// first. The resource given the first then carries the second's dependencies,
// and nothing reports it — both slices are the right length and every option
// in them is valid.
//
// It was latent here rather than live: all three call sites used each result
// immediately, so ordering hid it. That is not a reason to leave it — it is
// the reason a reviewer would not have caught the edit that woke it up.
//
// internal/pkg/pulumiopts.With is the replacement, and its own tests assert both
// halves: that it copies, and that plain append does not.
func TestNoAppendOntoASharedOptionSlice(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	var (
		offences []string
		scanned  int
	)

	require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".cache", "node_modules", "coverage":
				return filepath.SkipDir
			}

			return nil
		}

		if filepath.Ext(path) != ".go" {
			return nil
		}

		// The package that exists to explain the trap quotes it, and its
		// tests perform it on purpose to prove it is real.
		if strings.Contains(filepath.ToSlash(path), "internal/pkg/pulumiopts/") {
			return nil
		}

		// This file names the pattern it is looking for.
		if filepath.Base(path) == "resourceoptions_test.go" {
			return nil
		}

		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}

		scanned++

		for i, line := range strings.Split(string(raw), "\n") {
			if !appendOntoOptions.MatchString(line) || isComment(line) {
				continue
			}

			offences = append(offences, filepath.ToSlash(path)+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
		}

		return nil
	}))

	// The gate has to have looked at something. An empty scan collects no
	// offences and reads as a clean tree.
	assert.Positive(t, scanned, "no file was read; this test is checking nothing")

	assert.Empty(t, offences,
		"append onto an option slice the caller owns. Use pulumiopts.With, which copies:\n  %s",
		strings.Join(offences, "\n  "))
}
