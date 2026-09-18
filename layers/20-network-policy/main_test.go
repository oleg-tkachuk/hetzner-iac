package main

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
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
		Egress []struct {
			ToEntities []string `json:"toEntities"`
			ToFQDNs    []struct {
				MatchName string `json:"matchName"`
			} `json:"toFQDNs"`
			ToPorts []struct {
				Ports []struct {
					Port     string `json:"port"`
					Protocol string `json:"protocol"`
				} `json:"ports"`
			} `json:"toPorts"`
		} `json:"egress"`
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

// TestManifests_AreAllMatchedByOneGlob catches a policy that is committed and
// never applied.
//
// The allow glob was `[0-4]*.yaml` and 50-allow-acme.yaml was the fifth file.
// Nothing would have failed: ConfigGroup applies what the glob matches, an
// unmatched file is not an error, and the policy would have sat in the
// directory looking applied. Under a default deny that is a dropped flow whose
// allow rule exists, is reviewed, and is not in the cluster.
func TestManifests_AreAllMatchedByOneGlob(t *testing.T) {
	t.Parallel()

	every, err := filepath.Glob(filepath.Join("manifests", "*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, every, "no manifests found; this test is checking nothing")

	allows, err := filepath.Glob(AllowManifests)
	require.NoError(t, err)

	matched := map[string]bool{DenyManifest: true}
	for _, path := range allows {
		matched[path] = true
	}

	for _, path := range every {
		assert.True(t, matched[path],
			"%s is matched by neither %q nor the deny, so it is committed and applied never",
			path, AllowManifests)
	}

	// And the deny is not in the allow set, or enabling it would stop being a
	// decision.
	assert.NotContains(t, allows, DenyManifest,
		"the deny is matched by the allow glob, so it applies without the config switch")
}

// apiServerPolicy is the manifest that permits reaching the API server.
const apiServerPolicy = "20-allow-apiserver.yaml"

// TestAPIServerPolicy_NamesThePortsTheClusterListensOn is the half of the
// KubePrism contract that lives in YAML.
//
// Two constants decide where the API answers: hetzner.PortKubeAPI, which the
// firewall and the API load balancer are built from, and
// clusterref.KubePrismPort, which Talos listens on and Cilium is pointed at.
// This policy is what permits reaching either, and it is a committed manifest
// — no compiler compares its numbers to anything.
//
// The drift it exists for is silent and arrives late: while the default deny
// is off the policy is additive and a wrong port costs nothing, so the
// mismatch is introduced in one commit and discovered when the deny goes on,
// as every controller's API traffic dropped with every pod still Running.
func TestAPIServerPolicy_NamesThePortsTheClusterListensOn(t *testing.T) {
	t.Parallel()

	policies := manifests(t)[apiServerPolicy]
	require.Len(t, policies, 1,
		"%s holds no single policy: renamed, or split into documents this test reads past",
		apiServerPolicy)

	var permitted []string

	for _, rule := range policies[0].Spec.Egress {
		for _, destination := range rule.ToPorts {
			for _, port := range destination.Ports {
				permitted = append(permitted, port.Port)
			}
		}
	}

	listening := []string{
		strconv.Itoa(clusterspec.PortKubeAPI),
		strconv.Itoa(clusterspec.KubePrismPort),
	}

	slices.Sort(permitted)
	slices.Sort(listening)

	assert.Equal(t, listening, permitted,
		"%s permits %v while the cluster answers on %v", apiServerPolicy, permitted, listening)
}

// argoCDGitPolicy is the file, named once so the test below and a future
// reader agree on which policy is being argued about.
const argoCDGitPolicy = "70-allow-argocd-git.yaml"

// TestArgoCDGitPolicy_StaysBroadOnPurpose pins a decision that reads like an
// oversight.
//
// `toEntities: world` is the only one in this directory, and narrowing it to a
// list of names is the obvious tidy-up — both neighbouring policies do exactly
// that. It would be wrong here: Argo CD reaches the forge `gitops:repoURL`
// names AND every Helm or OCI registry any child Application references, so an
// allowlist needs editing each time an application is added. Each omission
// then reads as a broken application rather than as policy, which is how a
// gate becomes one people turn off.
//
// Both ports for the reason 50-allow-acme.yaml gives about staging and
// production: which one is used follows from a config key, and naming one
// turns the other into an outage the moment that key moves.
func TestArgoCDGitPolicy_StaysBroadOnPurpose(t *testing.T) {
	t.Parallel()

	policies, found := manifests(t)[argoCDGitPolicy]
	require.True(t, found, "%s is gone, and Argo CD cannot fetch under the deny", argoCDGitPolicy)
	require.Len(t, policies, 1)

	var (
		entities []string
		ports    []string
	)

	for _, rule := range policies[0].Spec.Egress {
		entities = append(entities, rule.ToEntities...)

		assert.Empty(t, rule.ToFQDNs,
			"%s has been narrowed to named hosts. Every chart an Application references "+
				"would have to be listed here, and each one missed reads as a broken "+
				"application rather than as policy", argoCDGitPolicy)

		for _, block := range rule.ToPorts {
			for _, port := range block.Ports {
				assert.Equal(t, "TCP", port.Protocol)

				ports = append(ports, port.Port)
			}
		}
	}

	assert.Contains(t, entities, "world",
		"%s no longer permits egress outside the cluster, which is the whole flow",
		argoCDGitPolicy)

	assert.ElementsMatch(t, []string{"443", "22"}, ports,
		"%s must name both: 443 for an https:// repository and every chart pull, 22 for "+
			"git@host:path. Which is used follows from gitops:repoURL", argoCDGitPolicy)
}
