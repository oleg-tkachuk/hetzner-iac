package clusterspec_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
)

func TestTopologyFile(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "cluster.prod.yaml", clusterspec.TopologyFile("prod"))
}

func TestTopologyStack(t *testing.T) {
	t.Parallel()

	// A name TopologyFile would not have built is never loaded by anything,
	// and sits in the repository looking like configuration that is in effect.
	for name, want := range map[string]string{
		"cluster.prod.yaml":    "prod",
		"cluster.dev.yaml":     "dev",
		"cluster.example.yaml": clusterspec.ExampleTopologyStack,
		// Strip the extension before the prefix: the other order turns
		// "cluster.yaml" into the stack "yaml".
		"cluster.yaml":          "",
		"cluster..yaml":         "",
		"cluster.prod.yml":      "",
		"cluster.prod.old.yaml": "",
		"Pulumi.yaml":           "",
		"prod.yaml":             "",
		"":                      "",
	} {
		stack, ok := clusterspec.TopologyStack(name)

		assert.Equal(t, want, stack, name)
		assert.Equal(t, want != "", ok, name)
	}
}

func TestTopologyStack_ReadsBackWhatTopologyFileBuilds(t *testing.T) {
	t.Parallel()

	for _, stack := range []string{"dev", "prod", "staging-2", clusterspec.ExampleTopologyStack} {
		got, ok := clusterspec.TopologyStack(clusterspec.TopologyFile(stack))

		assert.True(t, ok, stack)
		assert.Equal(t, stack, got)
	}
}
