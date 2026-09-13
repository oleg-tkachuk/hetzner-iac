// Command stack answers and settles questions about Pulumi stacks.
//
// It exists because one shell idiom appeared three times — in the cluster
// tier's init, in the platform layers' init, and in the preview workflow:
//
//	pulumi stack ls --json | jq -e --arg s "$STACK" 'any(.[]; .name == $s)'
//
// Three copies of a pipeline is three places to get it wrong, and it is the
// reason `jq` was a prerequisite at all.
//
// `ensure` does the whole decision rather than answering half of it. The
// obvious `pulumi stack init || pulumi stack select` is worse than it looks:
// it sends init's stderr to /dev/null to keep the "already exists" case quiet,
// so ANY init failure — a rejected stack tag, a bad token, no network —
// surfaces only as select complaining the stack does not exist. That cost a
// real diagnosis once: init was refusing a description over 256 characters,
// and the operator saw "no stack named 'dev' found".
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) != 3 {
		return fmt.Errorf("usage: stack <exists|ensure|ref> <project-dir> <stack>")
	}

	command, dir, name := args[0], args[1], args[2]

	switch command {
	case "exists":
		present, err := exists(ctx, dir, name)
		if err != nil {
			return err
		}

		if !present {
			// Listing what does exist, because the failure this replaces gave
			// only an exit code: eight jobs reported `exit code 6` at once
			// with nothing naming the stacks that were there.
			available, listErr := names(ctx, dir)
			if listErr != nil {
				return fmt.Errorf("no stack named %q in %s (and listing failed: %w)", name, dir, listErr)
			}

			return fmt.Errorf("no stack named %q in %s. Stacks that do exist: %s",
				name, dir, strings.Join(available, ", "))
		}

		return nil

	case "ensure":
		return ensure(ctx, dir, name)

	case "ref":
		return reference(ctx, dir, name)

	default:
		return fmt.Errorf("unknown command %q: exists, ensure or ref", command)
	}
}

// stackRow is the part of `pulumi stack ls --json` this needs.
type stackRow struct {
	Name string `json:"name"`
}

func exists(ctx context.Context, dir, name string) (bool, error) {
	out, err := pulumi(ctx, dir, "stack", "ls", "--json")
	if err != nil {
		return false, err
	}

	return stackNamed(out, name)
}

// stackNamed answers the question `jq -e 'any(.[]; .name == $s)'` answered,
// separated from the call so it can be tested without a Pulumi backend.
//
// An unparseable list is an error rather than "absent": the pipeline this
// replaces could not tell them apart, and "absent" would have made init try to
// create a stack that already exists.
func stackNamed(raw []byte, name string) (bool, error) {
	var rows []stackRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return false, fmt.Errorf("pulumi stack ls returned no usable json: %w", err)
	}

	for _, row := range rows {
		if row.Name == name {
			return true, nil
		}
	}

	return false, nil
}

// names lists the stacks a project has, for an error message that tells the
// operator what to pick instead.
func names(ctx context.Context, dir string) ([]string, error) {
	out, err := pulumi(ctx, dir, "stack", "ls", "--json")
	if err != nil {
		return nil, err
	}

	var rows []stackRow
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("pulumi stack ls returned no usable json: %w", err)
	}

	if len(rows) == 0 {
		return []string{"none"}, nil
	}

	available := make([]string, 0, len(rows))
	for _, row := range rows {
		available = append(available, row.Name)
	}

	return available, nil
}

// qualifiedSegments is how many parts a fully qualified stack name has:
// <org>/<project>/<stack>. Pulumi Cloud prints all three under -Q; a
// self-managed backend has no organization and prints one, which is a name
// pulumi.NewStackReference cannot resolve.
const qualifiedSegments = 3

// qualifiedName picks one stack out of `pulumi stack ls -Q --json` and returns
// the fully qualified name a layer's clusterStackRef needs.
//
// Separated from the call so it can be tested without a Pulumi backend, and
// matching on the last segment because -Q qualifies every row while the caller
// knows only the stack's own name.
//
// Reading the backend's answer rather than assembling one: the organization
// from `pulumi whoami` is a guess as soon as an account has two, and the
// project name would be a third copy of the grep on Pulumi.yaml.
func qualifiedName(raw []byte, name string) (string, error) {
	var rows []stackRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return "", fmt.Errorf("pulumi stack ls returned no usable json: %w", err)
	}

	available := make([]string, 0, len(rows))

	for _, row := range rows {
		segments := strings.Split(row.Name, "/")

		available = append(available, row.Name)

		if segments[len(segments)-1] != name {
			continue
		}

		if len(segments) != qualifiedSegments {
			return "", fmt.Errorf("the backend names this stack %q, not <org>/<project>/<stack>: "+
				"a self-managed backend has no organization to reference, so pass ref= explicitly", row.Name)
		}

		return row.Name, nil
	}

	if len(available) == 0 {
		return "", fmt.Errorf("no stack named %q, and the project has none", name)
	}

	return "", fmt.Errorf("no stack named %q. Stacks that do exist: %s", name, strings.Join(available, ", "))
}

// reference prints the stack reference for one stack, for a caller that has to
// point another project at it.
//
// `pulumi stack ls`, not `pulumi --stack <name> stack --show-name`: the latter
// falls back to the SELECTED stack when the name is empty, so a caller with an
// unset variable gets a confident answer about the wrong stack.
func reference(ctx context.Context, dir, name string) error {
	out, err := pulumi(ctx, dir, "stack", "ls", "-Q", "--json")
	if err != nil {
		return err
	}

	ref, err := qualifiedName(out, name)
	if err != nil {
		return fmt.Errorf("%s: %w", dir, err)
	}

	fmt.Println(ref)

	return nil
}

// The one word `ensure` writes to stdout, so a caller can put it in a column
// of its own table rather than parse a sentence out of it. platform:init runs
// this six times, and six sentences naming an absolute path is most of what
// that command used to print.
const (
	StateCreated  = "created"
	StateExisting = "existing"
)

// stackAction pairs the pulumi subcommand with the word that describes it.
//
// Separated from ensure so the pairing can be tested without a pulumi binary:
// reporting "created" for a stack that was only selected is a wrong answer in
// the one place an operator looks to see whether a stack is new.
func stackAction(present bool) (verb, state string) {
	if present {
		return "select", StateExisting
	}

	return "init", StateCreated
}

// ensure selects the stack, creating it first if it is not there, and names
// which of those two it did.
func ensure(ctx context.Context, dir, name string) error {
	present, err := exists(ctx, dir, name)
	if err != nil {
		return err
	}

	verb, state := stackAction(present)

	if _, err := pulumi(ctx, dir, "stack", verb, name); err != nil {
		return err
	}

	fmt.Println(state)

	return nil
}

// pulumi runs the CLI in dir, returning stdout and folding stderr into the
// error so the CLI's own diagnosis reaches the operator.
func pulumi(ctx context.Context, dir string, args ...string) ([]byte, error) {
	// #nosec G204,G702 -- args are literals from this file plus a stack name
	// from the Taskfile, and CommandContext takes an argument vector: there is
	// no shell to interpret any of it. The taint analysis cannot see that the
	// vector form is the mitigation.
	cmd := exec.CommandContext(ctx, "pulumi", append([]string{"--non-interactive"}, args...)...)
	cmd.Dir = dir

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			return nil, fmt.Errorf("pulumi %s: %w", strings.Join(args, " "), err)
		}

		return nil, fmt.Errorf("pulumi %s: %w\n%s", strings.Join(args, " "), err, message)
	}

	return out, nil
}
