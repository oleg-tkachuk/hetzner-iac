package ci

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// declaredKey matches one entry in a Pulumi.yaml `config:` block — two leading
// spaces, `project:key`, and nothing else on the line.
var declaredKey = regexp.MustCompile(`(?m)^  ([a-z-]+):(\w+):$`)

// configRead matches a stack config read, capturing either the literal key or
// the identifier holding it. Both forms are in use: most layers pass a string,
// 20-network-policy passes EnabledKey.
// The receiver is matched case-insensitively: a layer reads through the
// runner's `r.Cfg`, and internal/pkg/layer through a local `cfg`.
var configRead = regexp.MustCompile(
	`(?:(?i:cfg)\.(?:GetBool|GetInt|Get|RequireSecret|Require)|StringOr)\(\s*(?:"(\w+)"|([A-Z]\w+))`)

// tableRow matches a `project:key` in the first column of a markdown table.
var tableRow = regexp.MustCompile("(?m)^\\| `([a-z-]+|<layer>):(\\w+)` \\|")

// deliberatelyUndeclared are keys a layer reads and its Pulumi.yaml does not
// declare, on purpose, with the reason written in that Pulumi.yaml. The value
// is text that must still be there: an exception whose reason has been deleted
// is no longer an exception.
var deliberatelyUndeclared = map[string]string{
	"node-platform:hcloudToken": "deliberately NOT declared",
}

// configTables are the two places stack config is written down for a reader.
// configuration.md is the full reference and must name every declared key;
// README.md is a summary and may name fewer, but neither may name a key that
// does not exist.
var configTables = []struct {
	path       string
	exhaustive bool
}{
	{filepath.Join("docs", "configuration.md"), true},
	{"README.md", false},
}

// layerProject returns each layer's Pulumi project name and the directory it
// lives in, read from the Pulumi.yaml rather than assumed from the directory —
// the two differ on purpose (`50-gitops` is project `gitops`).
func layerProjects(t *testing.T) map[string]string {
	t.Helper()

	root := filepath.Join("..", "..")

	entries, err := os.ReadDir(filepath.Join(root, "layers"))
	require.NoError(t, err)

	projects := map[string]string{}

	name := regexp.MustCompile(`(?m)^name: (\S+)$`)

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		dir := filepath.Join(root, "layers", entry.Name())

		project, err := os.ReadFile(filepath.Join(dir, "Pulumi.yaml"))
		require.NoError(t, err)

		match := name.FindSubmatch(project)
		require.NotNil(t, match, "layers/%s has no `name:` in its Pulumi.yaml", entry.Name())

		projects[string(match[1])] = dir
	}

	require.NotEmpty(t, projects, "no layers found; this test is checking nothing")

	return projects
}

// goSources concatenates every non-test Go file in dir. Config reads are in the
// layer's own code, and a test fixture naming a key proves nothing about it.
func goSources(t *testing.T, dir string) string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	var all strings.Builder

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		body, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)

		all.Write(body)
	}

	return all.String()
}

// keysRead returns the stack config keys the Go in dir reads, resolving an
// identifier to the string constant it is assigned in the same directory.
func keysRead(t *testing.T, dir string) map[string]bool {
	t.Helper()

	body := goSources(t, dir)
	keys := map[string]bool{}

	for _, match := range configRead.FindAllStringSubmatch(body, -1) {
		if literal := match[1]; literal != "" {
			keys[literal] = true

			continue
		}

		// An identifier: the key is spelled once as a constant, which is the
		// pattern this repository prefers for a name two places must match.
		constant := regexp.MustCompile(match[2] + `\s*=\s*"(\w+)"`).FindStringSubmatch(body)
		require.NotNil(t, constant,
			"%s reads config through %s and no string constant of that name is in the same package",
			dir, match[2])

		keys[constant[1]] = true
	}

	return keys
}

// TestConfigKeys_DeclaredAndReadAreTheSameSet holds a layer's Pulumi.yaml to
// the config its code actually reads, in both directions.
//
// Each direction fails silently in its own way. A key the code reads and the
// Pulumi.yaml does not declare is missing from `pulumi config`, so the only
// description of it is whatever prose someone remembered to write — and
// `Cfg.Get` returns empty for a key nobody knew to set. A key declared and read
// by nothing is worse: `pulumi config set` accepts it, prints nothing, and the
// operator has every reason to believe the setting took effect.
func TestConfigKeys_DeclaredAndReadAreTheSameSet(t *testing.T) {
	t.Parallel()

	// clusterStackRef is read by internal/pkg/layer on every layer's behalf rather than
	// by the layer itself, so it would look unread from inside the directory.
	shared := keysRead(t, filepath.Join("..", "..", "internal", "pkg", "layer"))
	require.NotEmpty(t, shared, "internal/pkg/layer reads no config; the exemption below is hiding a real gap")

	for project, dir := range layerProjects(t) {
		pulumiYAML, err := os.ReadFile(filepath.Join(dir, "Pulumi.yaml"))
		require.NoError(t, err)

		declared := map[string]bool{}

		for _, match := range declaredKey.FindAllStringSubmatch(string(pulumiYAML), -1) {
			assert.Equal(t, project, match[1],
				"%s declares %q under a project prefix that is not its own, so Pulumi ignores it",
				dir, match[1]+":"+match[2])

			declared[match[2]] = true
		}

		read := keysRead(t, dir)

		for key := range declared {
			assert.True(t, read[key] || shared[key],
				"%s declares %q and nothing reads it: `pulumi config set %s:%s` would be accepted and do nothing",
				dir, key, project, key)
		}

		for key := range read {
			qualified := project + ":" + key
			if reason, ok := deliberatelyUndeclared[qualified]; ok {
				assert.Contains(t, string(pulumiYAML), reason,
					"%s is exempt from being declared and %s no longer says why",
					qualified, filepath.Join(dir, "Pulumi.yaml"))

				continue
			}

			assert.True(t, declared[key],
				"%s reads %q and its Pulumi.yaml does not declare it, so `pulumi config` cannot describe it",
				dir, key)
		}
	}
}

// TestConfigKeys_TheTablesNameKeysThatExist holds the documented stack config
// to the declared stack config.
//
// This is the one that had a failure in it. Both tables listed `gitops:domain`
// as a key of 50-gitops; no such key exists and none ever did — the layer reads
// `metadata.domain` from the cluster tier, deliberately, so that 40-ingress and
// 50-gitops cannot spell one hostname two ways. An operator following the table
// would have run `pulumi config set gitops:domain …`, been told nothing, and
// got no Ingress.
func TestConfigKeys_TheTablesNameKeysThatExist(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	declared := map[string]bool{}

	for project, dir := range layerProjects(t) {
		pulumiYAML, err := os.ReadFile(filepath.Join(dir, "Pulumi.yaml"))
		require.NoError(t, err)

		for _, match := range declaredKey.FindAllStringSubmatch(string(pulumiYAML), -1) {
			declared[project+":"+match[2]] = true
		}
	}

	require.NotEmpty(t, declared, "no layer declares any config key; this test is checking nothing")

	projects := layerProjects(t)

	for _, table := range configTables {
		body, err := os.ReadFile(filepath.Join(root, table.path))
		require.NoError(t, err)

		documented := map[string]bool{}

		for _, match := range tableRow.FindAllStringSubmatch(string(body), -1) {
			project, key := match[1], match[2]

			// `<layer>:clusterStackRef` stands for every layer's own copy, and
			// `hcloud:token` belongs to the provider rather than to a layer.
			if project == "<layer>" {
				for name := range projects {
					documented[name+":"+key] = true
				}

				continue
			}

			if _, ok := projects[project]; !ok {
				continue
			}

			qualified := project + ":" + key
			documented[qualified] = true

			if _, exempt := deliberatelyUndeclared[qualified]; exempt {
				continue
			}

			assert.True(t, declared[qualified],
				"%s documents %q and no layer declares it, so setting it does nothing",
				table.path, qualified)
		}

		if !table.exhaustive {
			continue
		}

		for key := range declared {
			assert.True(t, documented[key],
				"%s is the full stack config reference and does not mention %q",
				table.path, key)
		}
	}
}
