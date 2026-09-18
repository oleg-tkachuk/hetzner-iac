// Package secretout is the rule two commands share: cluster credentials do not
// go to a terminal.
//
// `tools/secrets` prints the Talos secrets bundle and `tools/recoverykit`
// prints that plus everything needed to use it. Both refuse a terminal, and
// both refused it with their own copy of the same three parts — the test, the
// sentinel and the message — which is three places for one decision to drift.
//
// The rule itself: a certificate authority in scrollback outlives the session,
// is copied into whatever the terminal emulator persists, and is searched for
// by anything reading the shell's history. Piping it into a password store is
// the only use that ends with it somewhere it can be found on purpose.
package secretout

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/term"
)

// ErrTerminal is the sentinel a caller and its tests match on, so neither has
// to match the prose.
var ErrTerminal = errors.New("refusing to print a cluster credential to a terminal")

// IsTerminal answers whether what a command prints lands in scrollback.
//
// x/term rather than a ModeCharDevice test on Stat: /dev/null is a character
// device too, so that test refuses a redirect to it — the right outcome for the
// wrong reason, and the wrong outcome for anyone debugging with one. This asks
// the file descriptor whether it is a terminal.
func IsTerminal() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// Refuse is the whole refusal: what is being withheld, and the one command that
// keeps it instead.
//
// The remedy is part of the error rather than of the documentation, because
// this is read by somebody who has just typed the command and has nothing else
// in front of them.
func Refuse(what, task, entry string) error {
	return fmt.Errorf("%w: %s belongs somewhere that keeps it, for example\n\n  %s | pass insert -m %s",
		ErrTerminal, what, task, entry)
}
