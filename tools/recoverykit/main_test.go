package main

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRun_RefusesATerminal is the whole reason this is a tool and not a
// `pulumi stack output` in a taskfile: a certificate authority printed to a
// terminal outlives the session in scrollback, and the shell's history of the
// command says nothing about that.
func TestRun_RefusesATerminal(t *testing.T) {
	t.Parallel()

	err := run(context.Background(), []string{"dev"}, true)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrTerminal)

	// The message has to carry the pipe, because somebody who hits this is
	// mid-task and the next thing they do is retype the command.
	assert.Contains(t, err.Error(), "pass insert -m hetzner/dev/recovery-kit")
}

func TestRun_RefusesTheWrongArgumentCount(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{}, {"dev", "extra"}} {
		err := run(context.Background(), args, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "usage: recoverykit <stack>")
	}
}

// TestRun_RefusesAStackNameThatIsAPath is the check that let the path join
// stop being a taint rather than a silenced warning.
//
// The failure it prevents is a misreading, not an exploit: a name carrying a
// separator looks for a topology somewhere other than infra/cluster, misses,
// and is reported as a missing file — which reads as "this stack was never
// created" for a name that was simply typed wrong.
func TestRun_RefusesAStackNameThatIsAPath(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"../dev", "dev/../../etc", "a/b", ".hidden", "", "dev name",
	} {
		err := run(context.Background(), []string{name}, false)
		require.Error(t, err, "%q was accepted", name)
		assert.Contains(t, err.Error(), "is not a stack name", "%q", name)
	}

	// And the ordinary ones still pass the check. They fail later, at the
	// backend, which is not this test's business — so the assertion is only
	// that the refusal above is not what stopped them.
	for _, name := range []string{"dev", "prod-2", "eu_west", "v1.2"} {
		err := run(context.Background(), []string{name}, false)
		if err != nil {
			assert.NotContains(t, err.Error(), "is not a stack name", "%q", name)
		}
	}
}

// TestDocument_CarriesEveryPartAndWhatItOpens holds the document's contract.
//
// It is read once, years after it was written, by somebody who has lost the
// thing that would have explained it. So every part is labelled and every
// label says what it opens — the order matters too, because the bundle is
// useless without the snapshot and the snapshot is unreadable without the
// restic password.
func TestDocument_CarriesEveryPartAndWhatItOpens(t *testing.T) {
	t.Parallel()

	kit := Kit{
		Stack:    "dev",
		Bundle:   []byte("cluster:\n  ca:\n    crt: QUJD\n"),
		Backup:   []byte(`{"backupHost":"u1.your-storagebox.de"}`),
		Topology: []byte("metadata:\n  name: dev\n"),
	}

	doc := string(kit.Document())

	// The prose is wrapped, so a sentence spans a newline and an indent.
	// Asserting on the collapsed text keeps this about what the document says
	// rather than about where the lines happen to break.
	prose := collapse(doc)

	for _, want := range []string{
		"RECOVERY KIT · dev",
		"talos-secrets",
		"backup-outputs",
		"topology · cluster.dev.yaml",
	} {
		assert.Contains(t, doc, want)
	}

	for _, want := range []string{
		// Each part's consequence, not just its name.
		"snapshot restores nothing",
		"does not open the old repository",
		"names the networks it is administered from",
		// And what this is NOT, because the other half is a different task.
		"task cluster:state:export",
	} {
		assert.Contains(t, prose, want)
	}

	assert.Contains(t, doc, "crt: QUJD")
	assert.Contains(t, doc, "u1.your-storagebox.de")
	assert.Contains(t, doc, "name: dev")

	// The bundle first, then the password that decrypts what it restores,
	// then the shape to rebuild into.
	assert.Less(t, strings.Index(doc, "talos-secrets"), strings.Index(doc, "backup-outputs"))
	assert.Less(t, strings.Index(doc, "backup-outputs"), strings.Index(doc, "topology · cluster"))
}

// TestDocument_SaysWhatIsMissingRatherThanOmittingIt is the failure mode a
// backup has: it is written on a good day and read on a bad one.
//
// A kit with no backup outputs is still worth keeping — it holds the CA — but
// printing it without a word would leave the reader believing the snapshots
// are recoverable. So the gap is in the document, where they will see it, and
// not only in whatever scrolled past when it was taken.
func TestDocument_SaysWhatIsMissingRatherThanOmittingIt(t *testing.T) {
	t.Parallel()

	kit := Kit{
		Stack:           "dev",
		Bundle:          []byte("cluster: {}"),
		BackupMissing:   "no stack named dev",
		TopologyMissing: "open cluster.dev.yaml: no such file or directory",
	}

	doc := string(kit.Document())

	assert.Contains(t, doc, "MISSING — the snapshots on the Storage Box cannot be decrypted")
	assert.Contains(t, doc, "Apply layers/60-backup")
	assert.Contains(t, doc, "no stack named dev")

	assert.Contains(t, doc, "MISSING — no task in this repository runs without it")
	assert.Contains(t, doc, "no such file or directory")
}

// TestDocument_KeepsEverySectionMarkerOnItsOwnLine is what makes the parts
// separable again.
//
// A bundle that does not end in a newline would otherwise put the next marker
// on the same line as its last byte, and the person splitting this file years
// later is doing it with their eyes.
func TestDocument_KeepsEverySectionMarkerOnItsOwnLine(t *testing.T) {
	t.Parallel()

	kit := Kit{
		Stack:    "dev",
		Bundle:   []byte("no-trailing-newline"),
		Backup:   []byte(`{"a":1}`),
		Topology: []byte("b: 2"),
	}

	for _, line := range strings.Split(string(kit.Document()), "\n") {
		if idx := strings.Index(line, section); idx > 0 {
			t.Errorf("a section marker shares a line with %q", line[:idx])
		}
	}
}

// collapse folds every run of whitespace into one space, so an assertion about
// what the document says is not an assertion about how it wraps.
func collapse(text string) string {
	return strings.Join(strings.Fields(text), " ")
}
