package docs

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/ci/citest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tableRow matches a `project:key` in the first column of a markdown table.
var tableRow = regexp.MustCompile("(?m)^\\| `([a-z-]+|<layer>):(\\w+)` \\|")

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

// providerNamespaces are config namespaces that belong to a provider rather
// than to a project here, and so are declared by no Pulumi.yaml —
// `hcloud:token` is set per stack and cannot be declared at all.
var providerNamespaces = map[string]bool{"hcloud": true}

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

	root := repoRoot

	declared := map[string]bool{}

	for project, dir := range citest.LayerProjects(t, repoRoot) {
		pulumiYAML, err := os.ReadFile(filepath.Join(dir, "Pulumi.yaml"))
		require.NoError(t, err)

		for _, match := range citest.DeclaredKey.FindAllStringSubmatch(string(pulumiYAML), -1) {
			declared[project+":"+match[2]] = true
		}
	}

	require.NotEmpty(t, declared, "no layer declares any config key; this test is checking nothing")

	projects := citest.LayerProjects(t, repoRoot)

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
				// A provider's own key, which no project here declares. Any
				// other prefix is a project that does not exist, and skipping
				// it is how every `backup:` row went unchecked.
				assert.True(t, providerNamespaces[project],
					"%s documents %q under %q, which is neither a project here nor a provider",
					table.path, key, project)

				continue
			}

			qualified := project + ":" + key
			documented[qualified] = true

			if _, exempt := citest.DeliberatelyUndeclared[qualified]; exempt {
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
