package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/layer/layertest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// policy is the part of a Cilium policy these tests read.
type policy struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Description       string `json:"description"`
		EnableDefaultDeny *struct {
			Ingress *bool `json:"ingress"`
			Egress  *bool `json:"egress"`
		} `json:"enableDefaultDeny"`
	} `json:"spec"`
}

// manifests reads every policy document in the directory, by file.
func manifests(t *testing.T) map[string][]policy {
	t.Helper()

	entries, err := os.ReadDir("manifests")
	require.NoError(t, err)
	require.NotEmpty(t, entries, "no policies — this layer would deploy nothing")

	out := map[string][]policy{}

	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join("manifests", entry.Name()))
		require.NoError(t, err)

		for _, document := range strings.Split(string(raw), "\n---") {
			if strings.TrimSpace(document) == "" {
				continue
			}

			var parsed policy
			require.NoError(t, yaml.Unmarshal([]byte(document), &parsed), entry.Name())

			out[entry.Name()] = append(out[entry.Name()], parsed)
		}
	}

	return out
}

func TestAllowPolicies_NeverEnableDefaultDeny(t *testing.T) {
	t.Parallel()

	// The property that makes this layer safe to apply to a running cluster.
	//
	// In Cilium a policy that selects an endpoint switches it to default-deny
	// for the directions the policy mentions — so an allow rule without this
	// field explicitly false does not merely permit, it starts dropping
	// everything it does not name. That is the whole hazard, and it is
	// invisible in a diff that only adds permissions.
	for file, policies := range manifests(t) {
		if strings.HasPrefix(file, "90-") {
			continue
		}

		for _, p := range policies {
			require.NotNil(t, p.Spec.EnableDefaultDeny,
				"%s/%s omits enableDefaultDeny, so applying it starts denying traffic",
				file, p.Metadata.Name)

			require.NotNil(t, p.Spec.EnableDefaultDeny.Ingress, "%s/%s", file, p.Metadata.Name)
			require.NotNil(t, p.Spec.EnableDefaultDeny.Egress, "%s/%s", file, p.Metadata.Name)

			assert.False(t, *p.Spec.EnableDefaultDeny.Ingress, "%s/%s", file, p.Metadata.Name)
			assert.False(t, *p.Spec.EnableDefaultDeny.Egress, "%s/%s", file, p.Metadata.Name)
		}
	}
}

func TestDefaultDeny_IsTheOnlyPolicyThatDenies(t *testing.T) {
	t.Parallel()

	// And it is the one the component keeps behind a config key.
	found := manifests(t)["90-default-deny.yaml"]
	require.Len(t, found, 1)

	deny := found[0]
	require.NotNil(t, deny.Spec.EnableDefaultDeny)
	assert.True(t, *deny.Spec.EnableDefaultDeny.Ingress)
	assert.True(t, *deny.Spec.EnableDefaultDeny.Egress)

	// The allow glob must not pick it up, or the deny would arrive with the
	// policies that are meant to be harmless.
	matched, err := filepath.Glob(AllowManifests)
	require.NoError(t, err)
	assert.NotContains(t, matched, DenyManifest,
		"the allow glob includes the deny, which would apply it unasked")
	assert.NotEmpty(t, matched, "the allow glob matches nothing")
}

func TestPolicies_AreClusterwideAndDescribed(t *testing.T) {
	t.Parallel()

	// Clusterwide because these are platform rules, not one namespace's. And
	// a description because `kubectl get ccnp` shows nothing else about what a
	// policy is for, and the next reader is debugging a dropped packet.
	for file, policies := range manifests(t) {
		for _, p := range policies {
			assert.Equal(t, "CiliumClusterwideNetworkPolicy", p.Kind, file)
			assert.NotEmpty(t, p.Spec.Description, "%s/%s has no description", file, p.Metadata.Name)
		}
	}
}

func TestKubeletProbesAreAllowedFirst(t *testing.T) {
	t.Parallel()

	// Measured as the cluster's dominant flow: `reserved:host -> pod`, the
	// kubelet running liveness and readiness probes. Deny it and every
	// workload restarts for ever, which is why it is 00- and why this test
	// names it rather than trusting the ordering of a directory listing.
	raw, err := os.ReadFile(filepath.Join("manifests", "00-allow-host.yaml"))
	require.NoError(t, err)

	assert.Contains(t, string(raw), "- host")
	assert.Contains(t, string(raw), "- remote-node",
		"three control-plane nodes send probes from each other")
}

func TestDenyRequested(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		value   string
		enabled bool
		fails   bool
	}{
		{value: "", enabled: false},
		{value: "false", enabled: false},
		{value: "0", enabled: false},
		{value: "true", enabled: true},
		{value: "True", enabled: true},
		{value: "1", enabled: true},
		// The case this function exists for: a plausible spelling that
		// would otherwise read as "off" and look identical to a cluster
		// nobody has enabled it on.
		{value: "yes", fails: true},
		{value: "tru", fails: true},
	} {
		got, err := denyRequested(tc.value)

		if tc.fails {
			require.Error(t, err, "%q", tc.value)
			assert.Contains(t, err.Error(), EnabledKey)

			continue
		}

		require.NoError(t, err, "%q", tc.value)
		assert.Equal(t, tc.enabled, got, "%q", tc.value)
	}
}

func TestComponents(t *testing.T) {
	t.Parallel()

	layertest.Check(t, Components)
}

func TestComponents_TheDenyFollowsTheAllows(t *testing.T) {
	t.Parallel()

	// Applied in the other order, the cluster spends the gap between them
	// dropping everything.
	for _, component := range Components {
		if component.Name == "default-deny" {
			assert.Contains(t, component.After, "allow")

			return
		}
	}

	t.Fatal("no default-deny component")
}
