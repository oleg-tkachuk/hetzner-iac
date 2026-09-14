package repo

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/platform"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// retiredIngressClass is the controller this platform replaced.
//
// Kept as a constant so the test names what it is looking for, rather than a
// bare string three assertions read differently.
const retiredIngressClass = "nginx"

// gateFile is this file, excluded from its own scan.
const gateFile = "ingressclass_test.go"

// codeExtensions are the files where a literal decides behaviour. Prose is
// excluded deliberately — see the test.
var codeExtensions = map[string]bool{
	".go":   true,
	".yaml": true,
	".yml":  true,
	".tmpl": true,
	".json": true,
}

// TestNoRetiredIngressClassInCode fails if the name of the replaced ingress
// controller appears anywhere a literal is executed.
//
// It exists because the literal has already escaped twice. It was `"nginx"` in
// layers/50-gitops, which is why pkg/platform.IngressClass exists at all; the
// fix there left an identical copy in layers/30-cluster-services' ACME solver,
// where it survived because nothing looked. That second copy would have made
// every certificate order hang: cert-manager creates an Ingress for the
// HTTP-01 challenge, no controller owns a class nobody registered, Let's
// Encrypt never reaches /.well-known/acme-challenge/, and the order sits
// pending with no error to read.
//
// A test of layer 30 alone would not have caught it — there was one, and it
// asserted the wrong value. This looks at the whole tree instead.
//
// COMMENTS ARE ALLOWED, and that is the design rather than an exemption. The
// history is worth keeping: pkg/platform and layers/50-gitops both explain why
// the constant exists by naming what it replaced, and deleting that would
// throw away the reason. Prose may remember; code may not.
func TestNoRetiredIngressClassInCode(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	var offences []string

	require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			// .git holds every past version of the file, including the ones
			// this test is about; node_modules and the Pulumi caches are not
			// ours to police.
			switch entry.Name() {
			case ".git", ".cache", "node_modules", "coverage":
				return filepath.SkipDir
			}

			return nil
		}

		if !codeExtensions[filepath.Ext(path)] {
			return nil
		}

		// This file names what it is looking for, so it cannot be subject to
		// its own rule. Skipped by name rather than by assembling the literal
		// from pieces, which would hide the one thing the file is about.
		if filepath.Base(path) == gateFile {
			return nil
		}

		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}

		for i, line := range strings.Split(string(raw), "\n") {
			if !strings.Contains(strings.ToLower(line), retiredIngressClass) {
				continue
			}

			if isComment(line) {
				continue
			}

			offences = append(offences, filepath.ToSlash(path)+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
		}

		return nil
	}))

	assert.Empty(t, offences,
		"%q appears in code, and this platform runs %s. Read the class from "+
			"pkg/platform.IngressClass — a class no controller owns is accepted by the "+
			"API server and then ignored, so nothing reports the mistake:\n  %s",
		retiredIngressClass, platform.IngressClass, strings.Join(offences, "\n  "))
}

// isComment reports whether a line is prose rather than code, in the two
// comment syntaxes this repository's code files use.
//
// Deliberately simple: it does not understand a comment that follows code on
// the same line, so `foo := "nginx" // why` is reported. That is the safe
// direction to be wrong in.
func isComment(line string) bool {
	trimmed := strings.TrimSpace(line)

	return strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#")
}

// TestIngressClassIsWhatTheChartInstalls holds pkg/platform's constant to the
// chart the ingress layer actually deploys.
//
// The constant is the single source of truth for every consumer, which only
// helps if it matches the controller. Swap the chart without this and every
// Ingress in the platform names a class nobody owns — the same silent failure,
// arrived at from the other direction.
func TestIngressClassIsWhatTheChartInstalls(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "pkg", "charts", "registry.go"))
	require.NoError(t, err)

	assert.Contains(t, string(raw), `"`+platform.IngressClass+`"`,
		"pkg/platform.IngressClass is %q and no chart by that name is in the registry",
		platform.IngressClass)
}
