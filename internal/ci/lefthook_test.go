package ci

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// lefthookConfig is the git hooks file at the repository root.
const lefthookConfig = "lefthook.yml"

// singleThreadEnv is what every Go-running hook job must set: hooks fire on a
// shared workstation, where a build fanned out over every core starves the
// rest of it.
var singleThreadEnv = map[string]string{"GOMAXPROCS": "1", "GOFLAGS": "-p=1"}

// runsGo matches a hook command that compiles, tests or formats Go.
var runsGo = regexp.MustCompile(`\bgo (run|test|vet|build)\b|\bgofmt\b`)

// hookJob is the part of a lefthook job these tests read.
type hookJob struct {
	Name string            `json:"name"`
	Run  string            `json:"run"`
	Env  map[string]string `json:"env"`
}

// singleThreadProblems names every Go job in the given hooks that is missing
// part of singleThreadEnv, and counts the Go jobs it checked.
func singleThreadProblems(hooks map[string][]hookJob) ([]string, int) {
	var problems []string

	checked := 0

	for hook, jobs := range hooks {
		for _, job := range jobs {
			if !runsGo.MatchString(job.Run) {
				continue
			}

			checked++

			for key, want := range singleThreadEnv {
				if job.Env[key] != want {
					problems = append(problems, hook+"/"+job.Name+" sets "+key+"="+job.Env[key]+", want "+want)
				}
			}
		}
	}

	return problems, checked
}

func TestSingleThreadProblems(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		hooks    map[string][]hookJob
		problems int
		checked  int
	}{
		{"single-threaded", map[string][]hookJob{"pre-push": {{Name: "test", Run: "go test ./...", Env: singleThreadEnv}}}, 0, 1},
		{"no env", map[string][]hookJob{"pre-push": {{Name: "vet", Run: "go vet ./..."}}}, 2, 1},
		{"half the env", map[string][]hookJob{"pre-commit": {{Name: "fmt", Run: "gofmt -l x.go", Env: map[string]string{"GOMAXPROCS": "1"}}}}, 1, 1},
		{"not Go", map[string][]hookJob{"pre-commit": {{Name: "zizmor", Run: "zizmor ."}}}, 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			problems, checked := singleThreadProblems(tt.hooks)
			assert.Len(t, problems, tt.problems, problems)
			assert.Equal(t, tt.checked, checked)
		})
	}
}

// TestLefthook_GoJobsRunOnOneThread holds every Go hook job to one thread.
func TestLefthook_GoJobsRunOnOneThread(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", lefthookConfig))
	require.NoError(t, err)

	var sections map[string]json.RawMessage
	require.NoError(t, yaml.Unmarshal(raw, &sections), lefthookConfig)

	hooks := map[string][]hookJob{}

	for name, section := range sections {
		// The scalar settings beside the hooks do not decode into a hook.
		var hook struct {
			Jobs []hookJob `json:"jobs"`
		}
		if json.Unmarshal(section, &hook) != nil {
			continue
		}

		hooks[name] = hook.Jobs
	}

	problems, checked := singleThreadProblems(hooks)
	assert.Empty(t, problems)
	assert.Positive(t, checked, "%s has no Go job, so nothing was checked", lefthookConfig)
}
