package ci

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hetznerProvider is the call that gives a project the power to write to
// Hetzner Cloud.
const hetznerProvider = "hetzner.NewProvider"

// writesToHetzner are the projects allowed to make that call, and the reason
// each one is.
//
// `infra/cluster` is deliberately absent, and the absence is the sharper half
// of the invariant. It never builds a provider: the token is declared in its
// own `Pulumi.yaml` as `hcloud:token`, so it uses the DEFAULT hcloud provider.
// It is the source. Everything in this list is a RECEIVER — a project that
// takes the token as a stack output and builds an explicit provider from it,
// which is the thing worth counting.
//
// A named list because docs/design.md makes a claim about the shape of this
// repository, and a third receiver appearing unnoticed would make that claim
// quietly less true. It is the same device as PodSecurityExemptNamespaces: an
// exception with its reason beside it, rather than a pattern that happens to
// hold. The document said something stronger and wrong for four months — "no
// layer holds a Hetzner credential" — which is what a claim with nothing
// checking it does.
var writesToHetzner = map[string]string{
	"infra/backup": "a tier of its own: a Storage Box and its subaccount, and nothing in Kubernetes. " +
		"Moved out of layers/ because a backup destination should be creatable before the cluster " +
		"and outlive it",
	"layers/40-ingress": "the ingress load balancer, which a CCM-managed Service cannot be: that was " +
		"tried against a live cluster and is invisible to plan and destroy, and refuses to target a " +
		"control-plane node",
}

// TokenConfigKey is where the cluster tier is configured with the token.
const TokenConfigKey = "hcloud:token"

// TestOnlyTheClusterTierIsConfiguredWithTheToken is the other half: one source,
// and every other project a receiver.
func TestOnlyTheClusterTierIsConfiguredWithTheToken(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	var configured []string

	for _, parent := range []string{"layers", "infra"} {
		entries, err := os.ReadDir(filepath.Join(root, parent))
		require.NoError(t, err)

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}

			raw, readErr := os.ReadFile(filepath.Join(root, parent, entry.Name(), "Pulumi.yaml"))
			require.NoError(t, readErr, entry.Name())

			if strings.Contains(string(raw), TokenConfigKey) {
				configured = append(configured, parent+"/"+entry.Name())
			}
		}
	}

	assert.Equal(t, []string{"infra/cluster"}, configured,
		"exactly one project may declare %s. A second one is a second place a credential is "+
			"configured, and destroying the cluster tier would no longer take the only "+
			"configured copy", TokenConfigKey)
}

// TestOnlyNamedProjectsWriteToHetzner keeps the seam a fact rather than a
// habit.
func TestOnlyNamedProjectsWriteToHetzner(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	var checked int

	for _, parent := range []string{"layers", "infra"} {
		entries, err := os.ReadDir(filepath.Join(root, parent))
		require.NoError(t, err)

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}

			project := parent + "/" + entry.Name()

			calls, err := callsHetznerProvider(t, filepath.Join(root, parent, entry.Name()))
			require.NoError(t, err, project)

			checked++

			if reason, allowed := writesToHetzner[project]; allowed {
				assert.True(t, calls,
					"%s is listed as writing to Hetzner (%q) and does not: drop it from the list, "+
						"because an exception nothing uses reads as a description of the code",
					project, reason)

				continue
			}

			assert.False(t, calls,
				"%s builds a Hetzner provider and is not in writesToHetzner. docs/design.md "+
					"claims the token reaches a project only as the cluster tier's output and that "+
					"nothing else is configured with one — add the project with its reason, or "+
					"reach Hetzner through Kubernetes as the other layers do",
				project)
		}
	}

	assert.Positive(t, checked, "no project directories found; this test is checking nothing")
}

// callsHetznerProvider reports whether a directory's non-test Go actually
// calls it.
//
// The syntax tree rather than a grep, because three projects DISCUSS the
// provider without building one — layers/10-node-platform explains why it
// writes the token into a Secret instead, and internal/pkg/hetzner documents
// the call itself. A text search reads those as writers, and this test exists
// to tell a comment from a call.
func callsHetznerProvider(t *testing.T, dir string) (bool, error) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}

	set := token.NewFileSet()
	called := false

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, parseErr := parser.ParseFile(set, filepath.Join(dir, name), nil, 0)
		if parseErr != nil {
			return false, parseErr
		}

		ast.Inspect(file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}

			pkg, ok := selector.X.(*ast.Ident)
			if !ok {
				return true
			}

			if pkg.Name+"."+selector.Sel.Name == hetznerProvider {
				called = true

				return false
			}

			return true
		})
	}

	return called, nil
}

// tierList matches the whitespace-separated list of tiers in the root taskfile,
// the same shape layerList matches for the layers.
var tierList = regexp.MustCompile(`(?s)TIERS: >-\n((?:    [^\n]+\n)+)`)

// TestTiers_MatchTheDirectories holds TIERS equal to what infra/ holds.
//
// The list exists because `policy:check` must cover every project, and a tier
// missing from it is a project the policy pack is never run over — which looks
// exactly like a clean run. That is not hypothetical: moving the backup tier
// out of layers/ took it out of the layers loop, and until the loop above it
// walked this list the pack stopped reaching it with nothing said.
func TestTiers_MatchTheDirectories(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	raw, err := os.ReadFile(filepath.Join(root, "Taskfile.yaml"))
	require.NoError(t, err)

	list := tierList.FindStringSubmatch(string(raw))
	require.NotNil(t, list, "no TIERS list in the root taskfile")

	named := strings.Fields(list[1])
	require.NotEmpty(t, named)

	entries, err := os.ReadDir(filepath.Join(root, "infra"))
	require.NoError(t, err)

	var found []string

	for _, entry := range entries {
		if entry.IsDir() {
			found = append(found, entry.Name())
		}
	}

	slices.Sort(named)
	slices.Sort(found)

	assert.Equal(t, found, named,
		"TIERS and the directories under infra/ disagree — a tier named here that does not "+
			"exist fails policy:check, and one that exists without being named is a project "+
			"the policy pack never runs over")
}

// clusterDestroy matches the destroy command in the cluster taskfile.
var clusterDestroy = regexp.MustCompile(`(?s)  destroy:\n.*?\n\n  [a-z]`)

// TestClusterDestroy_TakesTheProtectedResourcesAndKeepsTheCA holds the one
// command whose flags decide what survives a teardown.
//
// Three resources carry pulumi.Protect now: the Talos secrets bundle, the
// control-plane nodes and the API load balancer. A teardown is meant to take
// the last two, so `--exclude-protected` — which this command used while the
// bundle was the only protected thing — would leave a running cluster behind
// and report success. `--ignore-protect` takes them, and one `--exclude` keeps
// the bundle.
//
// The flags rather than the outcome, because the outcome needs a cluster. What
// the command does at runtime is checked there: it counts the resources its
// exclusion matches and refuses unless the answer is exactly one, because a
// `--exclude` that matches nothing is silent and would take the CA.
func TestClusterDestroy_TakesTheProtectedResourcesAndKeepsTheCA(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "tasks", "cluster.task.yaml"))
	require.NoError(t, err)

	// withoutComments, because the comment above the command explains that
	// --exclude-protected is NOT used — and the first version of this test read
	// that sentence as the flag being there. A mention is not a use.
	block := withoutComments(clusterDestroy.FindString(string(raw)))
	require.NotEmpty(t, block, "no destroy task in the cluster taskfile")

	assert.Contains(t, block, "--ignore-protect",
		"cluster:destroy cannot take the control plane without it, and a teardown that leaves "+
			"the servers running reports success")
	assert.NotContains(t, block, "--exclude-protected",
		"--exclude-protected now keeps the control plane and the API load balancer as well as "+
			"the CA, which is a teardown that does not tear down")
	assert.Contains(t, block, "talos:machine/secrets:Secrets",
		"the exclusion must name the secrets bundle's type; without it the destroy takes the "+
			"cluster CA, and Pulumi says nothing about an --exclude that matches nothing")
}
