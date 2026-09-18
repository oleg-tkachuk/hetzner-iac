package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixture is a taskfile holding one of each shape this has to read: a block
// scalar, a quoted one-liner, a `cmds` entry written as a mapping, a
// precondition, a status, a dynamic variable, and a defer.
const fixture = `version: "3"

vars:
  ROW: '%-10s %s'
  MACRO: |
    token="$(mint)"
    export token

tasks:
  blocks:
    preconditions:
      - sh: command -v mint >/dev/null
        msg: not installed
    status:
      - test -f done
    vars:
      COUNT:
        sh: |
          printf '%s\n' one
    cmds:
      - cmd: |
          set -euo pipefail
          printf '{{.ROW}}\n' a b
        defer: rm -f "{{.ROOT}}/tmp"
      - printf '%s\n' "{{.MISSING}}"
      - task: other
`

func parse(t *testing.T) Taskfile {
	t.Helper()

	file, err := Parse("Fixture.yaml", []byte(fixture))
	require.NoError(t, err)

	return file
}

// byTaskAndLine indexes the fixture's blocks so a case can name one without
// depending on the order the walk happens to visit them in.
func byTaskAndLine(file Taskfile) map[int]Block {
	at := map[int]Block{}
	for _, block := range file.Blocks {
		at[block.Line] = block
	}

	return at
}

func TestParse_FindsShellWhereverATaskfilePutsIt(t *testing.T) {
	t.Parallel()

	file := parse(t)
	at := byTaskAndLine(file)

	// Every line here is the line of the SHELL, counting the fixture from 1:
	// a block scalar's first command sits below its `|`, and a quoted scalar
	// sits on its own line.
	for line, want := range map[int]string{
		12: "command -v mint >/dev/null",   // preconditions[].sh
		15: "test -f done",                 // status[]
		19: "printf '%s\\n' one",           // vars.COUNT.sh
		22: "set -euo pipefail",            // cmds[].cmd, a block scalar
		24: `rm -f "{{.ROOT}}/tmp"`,        // cmds[].defer
		25: `printf '%s\n' "{{.MISSING}}"`, // cmds[], a plain scalar
	} {
		block, ok := at[line]
		require.True(t, ok, "no shell extracted at line %d; extracted %v", line, at)
		assert.Contains(t, block.Shell, want, "line %d", line)
		assert.Equal(t, "blocks", block.Task, "line %d", line)
	}

	// `task: other` names a task rather than holding shell, so nothing is
	// extracted for it — a block of "other" would be checked as a command.
	for _, block := range file.Blocks {
		assert.NotContains(t, block.Shell, "other")
	}
}

func TestParse_ReadsTheTopLevelVariablesAndWhereTheyAreDeclared(t *testing.T) {
	t.Parallel()

	file := parse(t)

	assert.Equal(t, "%-10s %s", file.Vars["ROW"].Value)
	assert.Equal(t, 4, file.Vars["ROW"].Line)

	// The macro's first line, not the line of its `|`: a finding in it is
	// reported against one of these lines.
	assert.Equal(t, 6, file.Vars["MACRO"].Line)
	assert.Contains(t, file.Vars["MACRO"].Value, `token="$(mint)"`)
}

func TestResolve_SubstitutesWhatTheTaskfileKnowsAndNamesTheRest(t *testing.T) {
	t.Parallel()

	file := parse(t)

	cases := []struct {
		name  string
		shell string
		want  string
	}{{
		name:  "a single-line variable becomes its value, because the value is what decides the shell",
		shell: `printf '{{.ROW}}\n' a b`,
		want:  `printf '%-10s %s\n' a b`,
	}, {
		name:  "an unknown action becomes the variable",
		shell: `printf '%s\n' "{{.MISSING}}"`,
		want:  `printf '%s\n' "${TASK_TEMPLATE}"`,
	}, {
		name:  "a multi-line variable is left for the preamble rather than shifting every line below it",
		shell: `{{.MACRO}}`,
		want:  `${TASK_TEMPLATE}`,
	}, {
		name:  "two actions on one line stay two",
		shell: `cp "{{.ROOT}}/a" "{{.ROOT}}/b"`,
		want:  `cp "${TASK_TEMPLATE}/a" "${TASK_TEMPLATE}/b"`,
	}, {
		// The regression this had: the replacement contains `${...}`, which
		// ReplaceAllString reads as a capture group. There is no group of that
		// name, so every action became the empty string — and the empty string
		// reads as findings about the taskfile. `[ "{{.x}}" != "yes" ]` was
		// reported as a constant expression and `{{.ROOT}}/.known_hosts` as
		// the path `/.known_hosts`.
		name:  "the substitution is a variable reference and not an empty string",
		shell: `[ "{{.trust}}" != "yes" ]`,
		want:  `[ "${TASK_TEMPLATE}" != "yes" ]`,
	}, {
		name:  "an action whose value is itself an action resolves once, to the variable",
		shell: `echo "{{.NESTED}}"`,
		want:  `echo "${TASK_TEMPLATE}"`,
	}}

	file.Vars["NESTED"] = Var{Value: "{{.stack}}", Line: 1}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, one.want, Resolve(one.shell, file.Vars))
		})
	}
}

func TestPreamble_AssignsTheVariableOnlyWhereSomethingUsesIt(t *testing.T) {
	t.Parallel()

	file := parse(t)

	// A block with no template in it: assigning the variable there is an
	// assignment nothing reads, and shellcheck reports one SC2034 per block.
	plain := Preamble(Block{Shell: "set -euo pipefail"}, file.Vars)
	assert.Equal(t, []PreambleLine{{Text: Shebang}, {Text: Dialect}}, plain)

	// A block that interpolates something unresolvable needs it, or every use
	// is an unassigned variable (SC2154).
	templated := Preamble(Block{Shell: `echo "{{.MISSING}}"`}, file.Vars)
	assert.Equal(t, []PreambleLine{{Text: Shebang}, {Text: Dialect}, {Text: Assignment}}, templated)

	// A block that interpolates only a resolvable variable does not: the
	// action is gone by the time shellcheck sees it.
	resolved := Preamble(Block{Shell: `printf '{{.ROW}}\n' a b`}, file.Vars)
	assert.Equal(t, []PreambleLine{{Text: Shebang}, {Text: Dialect}}, resolved)
}

func TestPreamble_CarriesAMultiLineVariableAndTheLinesItCameFrom(t *testing.T) {
	t.Parallel()

	file := parse(t)

	lines := Preamble(Block{Shell: "{{.MACRO}}\nuse \"$token\""}, file.Vars)

	var text, origins []string

	for _, line := range lines {
		text = append(text, line.Text)
		origins = append(origins, line.Text+"@"+itoa(line.Line))
	}

	// The macro's own shell is above the block, which is what makes `$token`
	// an assigned variable rather than SC2154 in every task that
	// interpolates it — and what gets the macro checked at all.
	assert.Contains(t, strings.Join(text, "\n"), `token="$(mint)"`)

	// And each of its lines carries the line it is declared on, so a finding
	// in the macro is reported at the macro rather than at whichever task
	// pulled it in.
	assert.Contains(t, origins, `token="$(mint)"@6`)
	assert.Contains(t, origins, `export token@7`)

	// The lines this program wrote itself carry no origin.
	assert.Equal(t, 0, lines[0].Line)
}

func TestScript_IsThePreambleThenTheResolvedShell(t *testing.T) {
	t.Parallel()

	file := parse(t)

	script := Script(Block{Shell: `printf '{{.ROW}}\n' a b`}, file.Vars)

	assert.Equal(t, Shebang+"\n"+Dialect+"\n"+`printf '%-10s %s\n' a b`+"\n", script)
}

func TestFindings_PutEveryCommentBackWhereTheTaskfileHasIt(t *testing.T) {
	t.Parallel()

	block := Block{Taskfile: "Fixture.yaml", Task: "blocks", Line: 100}
	preamble := []PreambleLine{
		{Text: Shebang},
		{Text: Dialect},
		{Text: Assignment},
		{Text: `token="$(mint)"`, Line: 6},
	}

	raw := `{"comments":[
	  {"file":"a.sh","line":5,"column":3,"level":"warning","code":2086,"message":"body"},
	  {"file":"a.sh","line":4,"column":8,"level":"warning","code":2155,"message":"preamble"},
	  {"file":"a.sh","line":1,"column":1,"level":"error","code":1071,"message":"synthetic"}
	]}`

	found, err := findings([]byte(raw), map[string]placed{"a.sh": {block: block, preamble: preamble}})
	require.NoError(t, err)
	require.Len(t, found, 3)

	at := map[string]int{}
	for _, one := range found {
		at[one.Message] = one.Line
	}

	// The first line of the block's own shell is the line after the preamble,
	// and it is the block's line — not one before it and not one after.
	assert.Equal(t, 100, at["body"], "a comment on the first shell line is the block's own line")

	// A comment in the multi-line variable is reported where the variable is
	// declared.
	assert.Equal(t, 6, at["preamble"])

	// A comment on a line this program wrote has nowhere of its own, so it
	// falls back to the block rather than being dropped or reported at 0.
	assert.Equal(t, 100, at["synthetic"])
}

func TestFindings_RefuseAFileTheyDidNotWrite(t *testing.T) {
	t.Parallel()

	_, err := findings([]byte(`{"comments":[{"file":"elsewhere.sh","line":1}]}`), map[string]placed{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "elsewhere.sh")
}

func TestFinding_NamesTheTaskOrSaysThereIsNone(t *testing.T) {
	t.Parallel()

	in := Finding{
		Block:   Block{Taskfile: "tasks/cluster.task.yaml", Task: "etcd:upload"},
		Line:    12,
		Column:  3,
		Level:   "warning",
		Code:    2086,
		Message: "Double quote",
	}

	assert.Equal(t, "tasks/cluster.task.yaml:12:3: etcd:upload: SC2086 (warning): Double quote", in.String())

	outside := in
	outside.Block.Task = ""
	assert.Contains(t, outside.String(), "(outside a task)")
}

// TestCheck_ReportsTheLineTheTaskfileHas runs the real shellcheck over a
// fixture with one known defect, which is the only way to prove the whole
// path: extraction, resolution, the preamble's length and the mapping back.
func TestCheck_ReportsTheLineTheTaskfileHas(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("shellcheck"); err != nil {
		t.Skip("shellcheck is not installed")
	}

	// Line 8 is `echo $unquoted` — the only line here shellcheck has anything
	// to say about.
	const defective = `version: "3"

tasks:
  one:
    cmds:
      - cmd: |
          unquoted="a b"
          echo $unquoted
`

	file, err := Parse("Defective.yaml", []byte(defective))
	require.NoError(t, err)

	found, err := check(context.Background(), []Taskfile{file})
	require.NoError(t, err)
	require.Len(t, found, 1)

	assert.Equal(t, 8, found[0].Line)
	assert.Equal(t, 2086, found[0].Code)
	assert.Equal(t, "one", found[0].Block.Task)
	assert.Equal(t, "Defective.yaml", found[0].Block.Taskfile)
}

// TestPaths_AreEveryTaskfileOnDisk keeps the literal list from falling behind
// the repository. A module this does not name is shell nothing reads, which is
// the state the whole tool exists to end.
func TestPaths_AreEveryTaskfileOnDisk(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	onDisk := []string{}

	entries, err := os.ReadDir(root)
	require.NoError(t, err)

	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "Taskfile.") {
			onDisk = append(onDisk, entry.Name())
		}
	}

	modules, err := filepath.Glob(filepath.Join(root, "tasks", "*.task.yaml"))
	require.NoError(t, err)

	for _, module := range modules {
		onDisk = append(onDisk, filepath.ToSlash(filepath.Join("tasks", filepath.Base(module))))
	}

	require.NotEmpty(t, onDisk, "no taskfile found, so this proved nothing")
	assert.ElementsMatch(t, onDisk, Paths,
		"Paths and the taskfiles on disk disagree: a taskfile this does not name is shell no linter reads")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}

	var digits []byte

	for ; n > 0; n /= 10 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
	}

	return string(digits)
}
