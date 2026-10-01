package ci

import (
	"encoding/json"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

const (
	// tasklibDepName is how renovate.json names the shared task library.
	tasklibDepName = "oleg-tkachuk/taskfiles"
	// trustTasklibScript replaces the library's checksums after a bump.
	trustTasklibScript = ".github/renovate/trust-tasklib.sh"
	// renovateWorkflow runs Renovate and holds its global configuration.
	renovateWorkflow = ".github/workflows/renovate.yaml"
	// allowedCommandsEnv is the global option postUpgradeTasks are matched against.
	allowedCommandsEnv = "RENOVATE_ALLOWED_COMMANDS"
	// containerTask is where Renovate's container finds the mounted Task binary.
	containerTask = "/usr/local/bin/task"
)

type postUpgradeTasks struct {
	Commands    []string `json:"commands"`
	FileFilters []string `json:"fileFilters"`
}

// tasklibPostUpgrade returns the post-upgrade task of the rule matching the
// task library.
func tasklibPostUpgrade(t *testing.T) postUpgradeTasks {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "renovate.json"))
	require.NoError(t, err)

	var config struct {
		PackageRules []struct {
			MatchDepNames    []string          `json:"matchDepNames"`
			PostUpgradeTasks *postUpgradeTasks `json:"postUpgradeTasks"`
		} `json:"packageRules"`
	}
	require.NoError(t, json.Unmarshal(raw, &config))

	for _, rule := range config.PackageRules {
		for _, name := range rule.MatchDepNames {
			if name == tasklibDepName && rule.PostUpgradeTasks != nil {
				return *rule.PostUpgradeTasks
			}
		}
	}

	t.Fatalf("no rule for %s has postUpgradeTasks, so a bump leaves its checksums stale", tasklibDepName)

	return postUpgradeTasks{}
}

// renovateStep returns the env and inputs of the step running the action.
func renovateStep(t *testing.T) (env, with map[string]string) {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("..", "..", renovateWorkflow))
	require.NoError(t, err)

	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Uses string            `json:"uses"`
				Env  map[string]string `json:"env"`
				With map[string]string `json:"with"`
			} `json:"steps"`
		} `json:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &workflow))

	for _, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if strings.HasPrefix(step.Uses, "renovatebot/github-action@") {
				return step.Env, step.With
			}
		}
	}

	t.Fatalf("%s has no renovatebot/github-action step", renovateWorkflow)

	return nil, nil
}

// TestRenovate_TasklibBumpReplacesItsChecksums holds renovate.json and the
// workflow together. Each half fails quietly on its own: a command missing
// from the allowed list is skipped with a warning, and a command without Task
// in the container fails inside Renovate, so the branch is left red either way.
func TestRenovate_TasklibBumpReplacesItsChecksums(t *testing.T) {
	t.Parallel()

	tasks := tasklibPostUpgrade(t)
	env, with := renovateStep(t)

	var allowed []string
	require.NoError(t, json.Unmarshal([]byte(env[allowedCommandsEnv]), &allowed),
		"%s must be a JSON array of regexes", allowedCommandsEnv)

	require.NotEmpty(t, tasks.Commands)

	for _, command := range tasks.Commands {
		matched := false

		for _, pattern := range allowed {
			if regexp.MustCompile(pattern).MatchString(command) {
				matched = true
			}
		}

		assert.True(t, matched, "%q matches nothing in %s, so Renovate skips it", command, allowedCommandsEnv)
	}

	assert.Contains(t, tasks.Commands, "bash "+trustTasklibScript)
	assert.FileExists(t, filepath.Join("..", "..", trustTasklibScript))

	checksum := remoteCache + "/git.github.com.go.0123.checksum"
	covered := false

	for _, filter := range tasks.FileFilters {
		ok, err := path.Match(filter, checksum)
		require.NoError(t, err, filter)

		covered = covered || ok
	}

	assert.True(t, covered, "fileFilters %v do not cover %s, so the new checksums are never committed",
		tasks.FileFilters, checksum)

	assert.Contains(t, with["docker-volumes"], ":"+containerTask+":ro",
		"Renovate's container does not ship Task; the workflow has to mount it")
}

// stubTask puts a task on PATH that logs its arguments and exits with code.
func stubTask(t *testing.T, code string) (bin, log string) {
	t.Helper()

	bin = t.TempDir()
	log = filepath.Join(t.TempDir(), "calls")
	stub := "#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >> " + log + "\nexit " + code + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "task"), []byte(stub), 0o700))

	return bin, log
}

// repoFixture lays out what the script touches: two entry points, a stale
// checksum and a cached taskfile that is not a checksum.
func repoFixture(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	cache := filepath.Join(dir, remoteCache)
	require.NoError(t, os.MkdirAll(cache, 0o700))

	for _, name := range []string{"Taskfile.yaml", devTaskfile} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), nil, 0o600))
	}

	for _, name := range []string{"git.github.com.go.old.checksum", "git.github.com.go.old.yaml"} {
		require.NoError(t, os.WriteFile(filepath.Join(cache, name), nil, 0o600))
	}

	return dir
}

func runTrustTasklib(t *testing.T, dir, bin string) error {
	t.Helper()

	script, err := filepath.Abs(filepath.Join("..", "..", trustTasklibScript))
	require.NoError(t, err)

	cmd := exec.CommandContext(t.Context(), "bash", script)
	cmd.Dir = dir

	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	return cmd.Run()
}

func TestTrustTasklib_ReplacesEveryChecksum(t *testing.T) {
	t.Parallel()

	dir := repoFixture(t)
	bin, log := stubTask(t, "0")

	require.NoError(t, runTrustTasklib(t, dir, bin))

	assert.NoFileExists(t, filepath.Join(dir, remoteCache, "git.github.com.go.old.checksum"),
		"a checksum for the old ref is a stale trust decision")
	assert.FileExists(t, filepath.Join(dir, remoteCache, "git.github.com.go.old.yaml"),
		"only checksums are removed")

	calls, err := os.ReadFile(log)
	require.NoError(t, err)
	assert.Equal(t,
		"--yes --taskfile Taskfile.dev.yaml --list\n--yes --taskfile Taskfile.yaml --list\n",
		string(calls), "every entry point loads, since each includes different modules")
}

func TestTrustTasklib_FailsWhenTaskFails(t *testing.T) {
	t.Parallel()

	dir := repoFixture(t)
	bin, _ := stubTask(t, "1")

	require.Error(t, runTrustTasklib(t, dir, bin),
		"a library Task cannot load must fail the upgrade, not commit no checksums")
}
