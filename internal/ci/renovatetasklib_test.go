package ci

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/ci/citest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

const (
	// tasklibDepName is how renovate.json names the shared task library.
	tasklibDepName = "oleg-tkachuk/taskfiles"
	// tasklibPreset is the library's own Renovate preset, which finds the pin
	// in any Taskfile with no annotation above it.
	tasklibPreset = "github>oleg-tkachuk/taskfiles//renovate/tasklib"
	// tasklibModule is the include that carries the library's lock tasks.
	tasklibModule = "tasklib"
	// tasklibLockCommand refreshes the lock after a bump. Through the dev
	// entry point, because that is the one including the module; --yes is the
	// trust decision, and the two flags after it override .taskrc.yml, which
	// keeps every other run offline.
	tasklibLockCommand = "task -t " + citest.DevTaskfile + " --yes --offline=false --download " + tasklibModule + ":lock"
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

	t.Fatalf("no rule for %s has postUpgradeTasks, so a bump leaves its lock stale", tasklibDepName)

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

// TestRenovate_TasklibBumpRefreshesItsLock holds renovate.json, the workflow
// and the dev entry point together. Each part fails quietly on its own: a
// command missing from the allowed list is skipped with a warning, a command
// without Task in the container fails inside Renovate, a filter that misses a
// file leaves it out of the commit, and a command naming a task no entry point
// has fails the same way — so the branch is left red in every case.
func TestRenovate_TasklibBumpRefreshesItsLock(t *testing.T) {
	t.Parallel()

	tasks := tasklibPostUpgrade(t)
	env, with := renovateStep(t)

	var allowed []string
	require.NoError(t, json.Unmarshal([]byte(env[allowedCommandsEnv]), &allowed),
		"%s must be a JSON array of regexes", allowedCommandsEnv)

	assert.Equal(t, []string{tasklibLockCommand}, tasks.Commands,
		"the lock is refreshed by the library's own task and by nothing else")

	for _, command := range tasks.Commands {
		matched := slices.ContainsFunc(allowed, func(pattern string) bool {
			return regexp.MustCompile(pattern).MatchString(command)
		})
		assert.True(t, matched, "%q matches nothing in %s, so Renovate skips it", command, allowedCommandsEnv)
	}

	// Both halves of the lock: the checksum is the trust decision, and the
	// copy beside it is what an offline run loads. The task replaces the
	// directory, so the same filter is what commits the removal of the old
	// ref's files.
	for _, extension := range []string{lockChecksum, lockTaskfile} {
		locked := remoteCache + "/git.github.com.go.0123" + extension
		covered := false

		for _, filter := range tasks.FileFilters {
			ok, err := path.Match(filter, locked)
			require.NoError(t, err, filter)

			covered = covered || ok
		}

		assert.True(t, covered, "fileFilters %v do not cover %s, so the new lock is never committed",
			tasks.FileFilters, locked)
	}

	assert.Contains(t, with["docker-volumes"], ":"+containerTask+":ro",
		"Renovate's container does not ship Task; the workflow has to mount it")

	raw, err := os.ReadFile(filepath.Join("..", "..", citest.DevTaskfile))
	require.NoError(t, err)

	assert.True(t, citest.IncludesOf(t, string(raw))[tasklibModule],
		"%s does not include %q, so `%s` names a task that does not exist",
		citest.DevTaskfile, tasklibModule, tasklibLockCommand)
}

// tasklibPresetPin is the shape the library's preset matches: `?ref=` on an include
// of taskfiles.git. A copy of the preset's own expression, which is fetched by
// Renovate and cannot be read from here; `tasklib:check` is the other reader
// of the same shape and fails when no entry point carries it.
var tasklibPresetPin = regexp.MustCompile(`github\.com/oleg-tkachuk/taskfiles\.git//[^?\s'"]*\?ref=[^\s'"&]+`)

// TestRenovate_FindsTheTasklibPinThroughThePreset holds the half of a bump
// that comes before the lock: somebody has to propose it.
//
// The preset replaced an annotation above each pin and a pattern of this
// repository's own. Dropping the preset from `extends` leaves the pins watched
// by nothing; restoring an annotation gives one line two managers. Both are
// silent. TestRenovateMatchesEveryTaskfilePin holds the second from the other
// side: the local manager may match the pulumi-kit pin and nothing else.
func TestRenovate_FindsTheTasklibPinThroughThePreset(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	raw, err := os.ReadFile(filepath.Join(root, ".github", "renovate.json"))
	require.NoError(t, err)

	var config struct {
		Extends []string `json:"extends"`
	}
	require.NoError(t, json.Unmarshal(raw, &config))

	assert.Contains(t, config.Extends, tasklibPreset,
		"renovate.json no longer extends the task library's preset, and no manager of "+
			"its own matches the pin — nobody proposes the next version")

	annotated := regexp.MustCompile(`# renovate:[^\n]*\n\s*TASKLIB:`)

	for _, name := range taskfileEntryPoints {
		entry, readErr := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, readErr, name)

		pin := tasklibPin.FindStringSubmatch(string(entry))
		require.NotNil(t, pin, "%s declares no TASKLIB", name)

		assert.Regexp(t, tasklibPresetPin, pin[1],
			"%s pins the task library in a shape the preset does not match, so Renovate "+
				"moves the other entry point and leaves this one", name)

		assert.NotRegexp(t, annotated, string(entry),
			"%s annotates TASKLIB for Renovate, and the preset already matches that line", name)
	}
}

// TestRenovate_EveryPostUpgradeCommandIsAllowed holds every rule's post-upgrade
// commands, not only the task library's, to the global allow-list. A command
// that matches none is skipped with a warning in a log nobody reads, and the
// branch it was meant to fix stays red.
func TestRenovate_EveryPostUpgradeCommandIsAllowed(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "renovate.json"))
	require.NoError(t, err)

	var config struct {
		PackageRules []struct {
			PostUpgradeTasks *postUpgradeTasks `json:"postUpgradeTasks"`
		} `json:"packageRules"`
	}
	require.NoError(t, json.Unmarshal(raw, &config))

	env, _ := renovateStep(t)

	var allowed []string
	require.NoError(t, json.Unmarshal([]byte(env[allowedCommandsEnv]), &allowed))

	var commands []string

	for _, rule := range config.PackageRules {
		if rule.PostUpgradeTasks != nil {
			commands = append(commands, rule.PostUpgradeTasks.Commands...)
		}
	}

	require.NotEmpty(t, commands, "no post-upgrade command in any rule; this test checks nothing")

	for _, command := range commands {
		matched := slices.ContainsFunc(allowed, func(pattern string) bool {
			return regexp.MustCompile(pattern).MatchString(command)
		})
		assert.True(t, matched, "%q matches nothing in %s, so Renovate skips it", command, allowedCommandsEnv)
	}
}
