// Command taskshell runs shellcheck over the shell inside the taskfiles.
//
// The gap it closes is one this repository has already argued for itself.
// .github/workflows/security.yaml says actionlint was added because "this
// repository carries a few hundred lines of shell inside YAML that no Go test
// and no linter was reading". That closed .github/workflows/. It did not close
// the taskfiles, which hold more shell than the workflows do — and the two
// longest blocks in them are cluster:etcd:upload and cluster:etcd:restore,
// the procedures that put a cluster back after it is gone.
//
// shellcheck cannot read a Taskfile by itself: the shell is YAML scalars with
// Go template actions in them, and `{{.ROOT}}` is not a word any shell parses.
// So each scalar is extracted, the actions are resolved as far as the taskfile
// itself resolves them, one file is written per block, and shellcheck's
// findings are mapped back to the line of the taskfile they came from.
//
// Extraction rather than a second copy of the shell kept beside the taskfile:
// a linter reading a copy proves something about the copy. This reads what
// Task runs.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// Paths are the taskfiles, in the order a report reads best: the two entry
// points, then the included modules alphabetically.
//
// A literal list rather than a glob, and TestPaths_AreEveryTaskfileOnDisk
// holds it to what is there. A glob would silently cover a new module, which
// sounds like the better failure right up until the new module is the one
// nobody looked at.
var Paths = []string{
	"Taskfile.yaml",
	"Taskfile.dev.yaml",
	"tasks/backup.task.yaml",
	"tasks/charts.task.yaml",
	"tasks/cluster.task.yaml",
	"tasks/platform.task.yaml",
	"tasks/policy.task.yaml",
}

// TemplateVariable is what an unresolvable template action becomes.
//
// A variable rather than a literal or an empty string, and the choice is
// load-bearing both ways. An empty substitution turns `"{{.ROOT}}"/bin` into
// `""/bin`, which parses as something else entirely. A literal turns
// `[ -n "{{.stack}}" ]` into a comparison between two constants, which
// shellcheck reports as SC2050 — a finding about this program rather than
// about the taskfile.
const TemplateVariable = "TASK_TEMPLATE"

// Shebang and Dialect head every extracted block.
//
// Task's own interpreter is mvdan/sh, which reads bash rather than POSIX sh —
// the taskfiles use `set -euo pipefail`, `[[` and `$'...'`, none of which is
// sh. Declared rather than left to a shebang, so the dialect is stated once
// here instead of depending on what shellcheck infers.
const (
	Shebang = "#!/usr/bin/env bash"
	Dialect = "# shellcheck shell=bash"
)

// Assignment gives TemplateVariable a value shellcheck cannot know.
//
// From a positional rather than a literal default, so nothing downstream is
// constant-folded, and emitted ONLY for a block that interpolates something:
// an assignment in a block with no templates in it is an unused variable, and
// shellcheck says so (SC2034) once per block.
var Assignment = TemplateVariable + `="${1:-}"`

// templateAction matches one Go template action. Non-greedy, so two actions on
// a line stay two actions.
var templateAction = regexp.MustCompile(`\{\{.*?\}\}`)

// simpleReference matches an action that is nothing but a variable — the only
// form whose value the taskfile itself can supply.
var simpleReference = regexp.MustCompile(`^\{\{\.([A-Za-z_][A-Za-z0-9_]*)\}\}$`)

// Taskfile is one file's shell and the variables it resolves.
type Taskfile struct {
	Path string
	// Vars is the file's top-level `vars`, scalars only.
	Vars map[string]Var
	// Blocks is every shell scalar in the file.
	Blocks []Block
}

// Var is a declared variable's value and where it is declared.
//
// The line is carried because a multi-line variable is shell in its own
// right: `_CL_CONTROL_PLANE` is four commands, six tasks interpolate it, and a
// finding in it belongs at its declaration rather than at each of the six.
type Var struct {
	Value string
	Line  int
}

// Block is one shell scalar, and where in the taskfile it came from.
type Block struct {
	// Taskfile is the path as Paths names it.
	Taskfile string
	// Task is the task the shell belongs to, spelled as `task` takes it.
	// Empty for shell outside any task, such as a global dynamic variable.
	Task string
	// Line is the first line of the shell ITSELF, 1-based — the first
	// command, not the key above it. A block scalar puts its `|` on one line
	// and its first command on the next, and a finding reported one line off
	// in a thirteen-hundred-line taskfile sends the reader to a comment.
	Line int
	// Shell is the scalar's contents, template actions and all.
	Shell string
}

// Finding is one shellcheck comment, placed back in the taskfile.
type Finding struct {
	Block   Block
	Line    int
	Column  int
	Level   string
	Code    int
	Message string
}

// String is the report line, in the file:line:column form an editor jumps to.
func (f Finding) String() string {
	task := f.Block.Task
	if task == "" {
		task = "(outside a task)"
	}

	return fmt.Sprintf("%s:%d:%d: %s: SC%d (%s): %s",
		f.Block.Taskfile, f.Line, f.Column, task, f.Code, f.Level, f.Message)
}

func run(ctx context.Context, args []string) error {
	if len(args) != 0 {
		return errors.New("usage: taskshell (no arguments; it reads the taskfiles this repository has)")
	}

	root, err := repositoryRoot(ctx)
	if err != nil {
		return err
	}

	files := make([]Taskfile, 0, len(Paths))
	blocks := 0

	for _, path := range Paths {
		raw, readErr := os.ReadFile(filepath.Join(root, path)) // #nosec G304 -- a path from Paths, a constant in this file
		if readErr != nil {
			return fmt.Errorf("read %s: %w", path, readErr)
		}

		parsed, parseErr := Parse(path, raw)
		if parseErr != nil {
			return parseErr
		}

		files = append(files, parsed)
		blocks += len(parsed.Blocks)
	}

	if blocks == 0 {
		return errors.New("no shell found in any taskfile, so this checked nothing")
	}

	findings, err := check(ctx, files)
	if err != nil {
		return err
	}

	for _, finding := range findings {
		fmt.Println(finding)
	}

	if len(findings) > 0 {
		return fmt.Errorf("%d shellcheck findings in %d shell blocks across %d taskfiles",
			len(findings), blocks, len(Paths))
	}

	fmt.Printf("%d shell blocks in %d taskfiles, no shellcheck findings\n", blocks, len(Paths))

	return nil
}

// Parse reads one taskfile's variables and shell.
//
// Through the YAML node tree rather than line by line, for the reason the node
// tree exists: a block scalar's indentation, its chomping indicator and a
// quoted scalar's escapes are the format's rules, and a scanner that
// reproduces them is right until the first taskfile written slightly
// differently. The node also carries the line, which is the only way a finding
// can name somewhere to look.
func Parse(path string, raw []byte) (Taskfile, error) {
	var document yaml.Node

	if err := yaml.Unmarshal(raw, &document); err != nil {
		return Taskfile{}, fmt.Errorf("parse %s: %w", path, err)
	}

	if len(document.Content) == 0 {
		return Taskfile{}, fmt.Errorf("%s is empty", path)
	}

	root := document.Content[0]

	file := Taskfile{Path: path, Vars: vars(root)}

	collect(path, "", root, &file.Blocks)

	return file, nil
}

// vars reads the file's top-level `vars` mapping.
//
// Top-level only. A task's own `vars` are scoped to it and would have to be
// tracked per block; the ones worth resolving are the shared ones, which is
// where the format strings and the shell macros live.
func vars(root *yaml.Node) map[string]Var {
	found := map[string]Var{}

	if root.Kind != yaml.MappingNode {
		return found
	}

	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "vars" || root.Content[i+1].Kind != yaml.MappingNode {
			continue
		}

		declared := root.Content[i+1]

		for j := 0; j+1 < len(declared.Content); j += 2 {
			if value := declared.Content[j+1]; value.Kind == yaml.ScalarNode {
				found[declared.Content[j].Value] = Var{
					Value: value.Value,
					Line:  firstContentLine(value),
				}
			}
		}
	}

	return found
}

// shellKeys are the keys whose value is shell, wherever they appear.
//
// Every one of them, not only the ones holding the long blocks: `sh` is a
// precondition and a dynamic variable, `status` decides whether a task runs at
// all, and shell that decides is shell that can be wrong. A uniform set also
// means there is nothing to remember when a task starts using a key it did not
// use before.
//
// `task` and `deps` are absent because they name tasks rather than shell, and
// a `cmds` entry written as a mapping is read through `cmd` for the same
// reason: the mapping form is how a `task:` reference carries its variables.
var shellKeys = map[string]bool{
	"cmd":    true,
	"cmds":   true,
	"sh":     true,
	"defer":  true,
	"status": true,
}

// collect walks a mapping, remembering which task it is inside.
func collect(path, task string, node *yaml.Node, into *[]Block) {
	if node.Kind != yaml.MappingNode {
		return
	}

	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i].Value, node.Content[i+1]

		switch {
		case key == "tasks" && value.Kind == yaml.MappingNode:
			// One level down is the task's name, which a report has to print:
			// a finding naming only the file leaves the reader searching a
			// thirteen-hundred-line document for it.
			for j := 0; j+1 < len(value.Content); j += 2 {
				collect(path, value.Content[j].Value, value.Content[j+1], into)
			}
		case shellKeys[key]:
			appendShell(path, task, value, into)
		default:
			descend(path, task, value, into)
		}
	}
}

// descend follows anything that is not itself shell, so a key nested under
// `vars` or inside a `cmds` mapping is still reached.
func descend(path, task string, node *yaml.Node, into *[]Block) {
	switch node.Kind {
	case yaml.MappingNode:
		collect(path, task, node, into)
	case yaml.SequenceNode:
		for _, item := range node.Content {
			descend(path, task, item, into)
		}
	case yaml.ScalarNode, yaml.AliasNode, yaml.DocumentNode:
	}
}

// appendShell takes a scalar as shell, and a sequence as a list of them.
func appendShell(path, task string, node *yaml.Node, into *[]Block) {
	switch node.Kind {
	case yaml.ScalarNode:
		if strings.TrimSpace(node.Value) != "" {
			*into = append(*into, Block{
				Taskfile: path,
				Task:     task,
				Line:     firstContentLine(node),
				Shell:    node.Value,
			})
		}
	case yaml.SequenceNode, yaml.MappingNode:
		for _, item := range node.Content {
			if item.Kind == yaml.ScalarNode {
				appendShell(path, task, item, into)

				continue
			}

			// A `cmds` entry is either a scalar of shell or a mapping whose
			// shell is under `cmd`. Descending rather than reading `cmd` here
			// is what also covers that mapping's own `defer`.
			descend(path, task, item, into)
		}
	case yaml.AliasNode, yaml.DocumentNode:
	}
}

// firstContentLine is where a scalar's own text begins.
//
// yaml reports the line the scalar TOKEN starts on, and for `|` and `>` that
// is the indicator, one line above the first command. Every other style —
// plain, single-quoted, double-quoted — starts token and text together.
func firstContentLine(node *yaml.Node) int {
	if node.Style == yaml.LiteralStyle || node.Style == yaml.FoldedStyle {
		return node.Line + 1
	}

	return node.Line
}

// Script is the block as a file shellcheck can read: the preamble, then the
// shell with its template actions resolved.
func Script(b Block, taskVars map[string]Var) string {
	preamble := Preamble(b, taskVars)

	lines := make([]string, 0, len(preamble))
	for _, line := range preamble {
		lines = append(lines, line.Text)
	}

	return strings.Join(lines, "\n") + "\n" + Resolve(b.Shell, taskVars) + "\n"
}

// PreambleLine is one line Script puts above the block, and the line of the
// taskfile it came from. Zero for a line this program wrote itself.
type PreambleLine struct {
	Text string
	Line int
}

// Preamble is what Script puts above the block, and its length is the offset
// between a line of the script and a line of the taskfile.
//
// Two of its three parts exist to keep shellcheck talking about the taskfile
// rather than about the extraction. The multi-line variables come first
// because they are the ones that assign: `_CL_CONTROL_PLANE` sets `nodes`, six
// tasks interpolate it and then loop over `$nodes`, and without its text above
// them every one of those loops reads an unassigned variable (SC2154). Putting
// it here also means that shell is checked at all, which it was not before.
func Preamble(b Block, taskVars map[string]Var) []PreambleLine {
	lines := []PreambleLine{{Text: Shebang}, {Text: Dialect}}

	if strings.Contains(Resolve(b.Shell, taskVars), "${"+TemplateVariable+"}") {
		lines = append(lines, PreambleLine{Text: Assignment})
	}

	for _, name := range sortedNames(taskVars) {
		declared := taskVars[name]
		if !strings.Contains(declared.Value, "\n") || !strings.Contains(b.Shell, "{{."+name+"}}") {
			continue
		}

		lines = append(lines, PreambleLine{Text: "# " + name})

		for i, text := range strings.Split(strings.TrimRight(Resolve(declared.Value, taskVars), "\n"), "\n") {
			lines = append(lines, PreambleLine{Text: text, Line: declared.Line + i})
		}
	}

	return lines
}

// Resolve substitutes the template actions in one scalar.
//
// A variable the taskfile declares as a single line is substituted for its
// value, because that value is what Task puts there and it is often what
// decides whether the shell is right: `_PL_ROW` is a printf format string, and
// with the action left in its place shellcheck sees a format with no
// conversions and reports that printf's arguments are ignored — true of the
// substitution, false of the taskfile.
//
// A multi-line value is not substituted inline, because it would shift every
// line below it and a finding would name the wrong one. Preamble puts those
// above the block instead.
//
// Anything else — a special variable like ROOT, a value passed on the command
// line, a `range` — becomes TemplateVariable. One pass, so a variable whose
// value is itself an action resolves to the variable rather than recursing.
func Resolve(shell string, taskVars map[string]Var) string {
	substituted := templateAction.ReplaceAllStringFunc(shell, func(action string) string {
		found := simpleReference.FindStringSubmatch(action)
		if found == nil {
			return action
		}

		declared, ok := taskVars[found[1]]
		if !ok || strings.Contains(declared.Value, "\n") {
			return action
		}

		return declared.Value
	})

	// `${VAR}` rather than `$VAR`: an action can be followed by anything, and
	// `$TASK_TEMPLATEinfra` is a different variable while `${TASK_TEMPLATE}infra`
	// is a substitution followed by a word, which is what Task does with it.
	//
	// Literal, because the replacement contains `${...}` and that is a capture
	// reference to Go's regexp: ReplaceAllString expanded `${TASK_TEMPLATE}`
	// as a group of that name, found none, and substituted nothing. Every
	// template action silently became the empty string, which then read as
	// findings about the taskfile — `[ "" != "yes" ]` reported as a constant
	// expression, and `{{.ROOT}}/.known_hosts` as the path `/.known_hosts`.
	return templateAction.ReplaceAllLiteralString(substituted, "${"+TemplateVariable+"}")
}

func sortedNames(taskVars map[string]Var) []string {
	names := make([]string, 0, len(taskVars))
	for name := range taskVars {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

// comments is shellcheck's json1 output.
type comments struct {
	Comments []struct {
		File    string `json:"file"`
		Line    int    `json:"line"`
		Column  int    `json:"column"`
		Level   string `json:"level"`
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"comments"`
}

// placed is one written file and what it has to be mapped back to.
type placed struct {
	block    Block
	preamble []PreambleLine
}

// check writes every block to a temporary directory and runs shellcheck once
// over all of them.
//
// One invocation rather than one per block: shellcheck starts in tens of
// milliseconds and there are over a hundred blocks, and its json1 output names
// the file each finding came from, so batching loses nothing.
func check(ctx context.Context, files []Taskfile) ([]Finding, error) {
	dir, err := os.MkdirTemp("", "taskshell")
	if err != nil {
		return nil, fmt.Errorf("make a temporary directory: %w", err)
	}

	// Kept when asked, because triaging a finding means reading the script
	// shellcheck read: a false positive here is a template this resolved
	// differently from the way Task does, and that is only visible in the
	// extracted file.
	if os.Getenv(KeepEnv) != "" {
		fmt.Fprintf(os.Stderr, "%s set, keeping the extracted blocks in %s\n", KeepEnv, dir)
	} else {
		// Discarded: the directory is under os.TempDir and holds nothing but
		// extracted shell. Failing the whole check because the cleanup of a
		// temporary directory failed would report the wrong problem.
		defer func() { _ = os.RemoveAll(dir) }()
	}

	written := map[string]placed{}

	var paths []string

	for _, file := range files {
		for i, block := range file.Blocks {
			path := filepath.Join(dir, fmt.Sprintf("%s-%04d.sh", strings.NewReplacer("/", "-", ".", "-").Replace(file.Path), i))

			if writeErr := os.WriteFile(path, []byte(Script(block, file.Vars)), 0o600); writeErr != nil {
				return nil, fmt.Errorf("write a block to %s: %w", path, writeErr)
			}

			written[path] = placed{block: block, preamble: Preamble(block, file.Vars)}
			paths = append(paths, path)
		}
	}

	raw, err := shellcheck(ctx, paths)
	if err != nil {
		return nil, err
	}

	return findings(raw, written)
}

// KeepEnv keeps the extracted blocks on disk instead of removing them.
const KeepEnv = "TASKSHELL_KEEP"

// ShellcheckFormat is the machine-readable output this parses. shellcheck's
// columns are laid out for a terminal and move between releases; json1 is a
// contract.
const ShellcheckFormat = "--format=json1"

// shellcheck runs the linter and returns its output.
func shellcheck(ctx context.Context, paths []string) ([]byte, error) {
	// #nosec G204 -- the arguments are a constant flag from this file plus
	// paths inside a directory this function just made with os.MkdirTemp.
	linter := exec.CommandContext(ctx, "shellcheck", append([]string{ShellcheckFormat}, paths...)...)

	out, err := linter.Output()
	if err != nil {
		var exit *exec.ExitError
		// shellcheck exits non-zero when it has findings, which is the normal
		// case here and not an error: the findings are on stdout.
		if errors.As(err, &exit) && len(out) > 0 {
			return out, nil
		}

		return nil, fmt.Errorf("run shellcheck (`brew install shellcheck`): %w", err)
	}

	return out, nil
}

// findings parses shellcheck's output and moves every comment back into the
// taskfile it came from.
func findings(raw []byte, written map[string]placed) ([]Finding, error) {
	var parsed comments

	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("parse shellcheck's output: %w", err)
	}

	found := make([]Finding, 0, len(parsed.Comments))

	for _, comment := range parsed.Comments {
		from, ok := written[comment.File]
		if !ok {
			return nil, fmt.Errorf("shellcheck reported on %s, which this did not write", comment.File)
		}

		line := from.block.Line + comment.Line - len(from.preamble) - 1

		if comment.Line <= len(from.preamble) {
			// A finding in the preamble is a finding in a multi-line variable,
			// and that variable is declared somewhere an operator can edit.
			// Reported there rather than at whichever of the tasks that
			// interpolate it happened to pull it in.
			line = from.preamble[comment.Line-1].Line
			if line == 0 {
				line = from.block.Line
			}
		}

		found = append(found, Finding{
			Block:   from.block,
			Line:    line,
			Column:  comment.Column,
			Level:   comment.Level,
			Code:    comment.Code,
			Message: comment.Message,
		})
	}

	sort.Slice(found, func(i, j int) bool {
		if found[i].Block.Taskfile != found[j].Block.Taskfile {
			return found[i].Block.Taskfile < found[j].Block.Taskfile
		}

		if found[i].Line != found[j].Line {
			return found[i].Line < found[j].Line
		}

		return found[i].Column < found[j].Column
	})

	return found, nil
}

// repositoryRoot is where the taskfiles are, whichever directory this was
// called from.
func repositoryRoot(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("find the repository root: %w", err)
	}

	return strings.TrimSpace(string(out)), nil
}
