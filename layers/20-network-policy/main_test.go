package main

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/charts"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/layer/layertest"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"

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
		Description string `json:"description"`
		// EndpointSelector is which endpoints a policy applies to. Read
		// because a policy is two halves — what it permits and to whom — and
		// a rule correct in one half and wide in the other is a rule that
		// permits the flow for everything in the cluster.
		EndpointSelector struct {
			MatchLabels map[string]string `json:"matchLabels"`
		} `json:"endpointSelector"`
		EnableDefaultDeny *struct {
			Ingress *bool `json:"ingress"`
			Egress  *bool `json:"egress"`
		} `json:"enableDefaultDeny"`
		Egress []rule `json:"egress"`
		// Ingress is read for the same reason Egress is: under the default
		// deny each direction is a separate decision, so a flow allowed one
		// way and not the other is a flow that does not work.
		Ingress []rule `json:"ingress"`
	} `json:"spec"`
}

// rule is one ingress or egress rule, in the fields these tests read. One type
// for both: the keys they share are the ones being asserted on, and two copies
// of it drifted the first time an ingress rule had to be read.
type rule struct {
	ToEntities    []string `json:"toEntities"`
	FromEndpoints []struct {
		MatchLabels map[string]string `json:"matchLabels"`
	} `json:"fromEndpoints"`
	ToFQDNs []struct {
		MatchName string `json:"matchName"`
	} `json:"toFQDNs"`
	ToPorts []struct {
		Ports []struct {
			Port     string `json:"port"`
			Protocol string `json:"protocol"`
		} `json:"ports"`
	} `json:"toPorts"`
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

// kedaPolicy holds KEDA's flows, and holds them inside the cluster.
const kedaPolicy = "80-allow-keda.yaml"

// TestKedaPolicy_StopsAtTheClusterEdge is the decision this file records, and
// the one a future change is most likely to undo without meaning to.
//
// KEDA can scale on an external source, and a cluster that does would need
// `toEntities: world` here — the argument 70-allow-argocd-git.yaml makes. This
// cluster scales on sources inside itself, so a scaler pointed at the internet
// should fail with a connection error rather than work by accident, and
// widening this should cost somebody a commit that says why.
func TestKedaPolicy_StopsAtTheClusterEdge(t *testing.T) {
	t.Parallel()

	policies, found := manifests(t)[kedaPolicy]
	require.True(t, found, "%s is gone, and KEDA reaches no scaler under the deny", kedaPolicy)
	require.Len(t, policies, 3, "%s holds the operator's egress and both sides of the gRPC flow", kedaPolicy)

	for _, policy := range policies {
		for _, rule := range policy.Spec.Egress {
			assert.Empty(t, rule.ToEntities,
				"%s permits egress to %v. KEDA's sources are inside this cluster; reaching "+
					"outside it is a separate decision and belongs in its own policy",
				kedaPolicy, rule.ToEntities)

			assert.Empty(t, rule.ToFQDNs,
				"%s names hosts outside the cluster, which is the same widening by another route",
				kedaPolicy)
		}
	}
}

// TestKedaPolicy_NamesTheGRPCPortTheChartRenders keeps the metrics path from
// drifting from the chart.
//
// The metrics API server answers kube-apiserver and reads the scaler values
// from the operator over gRPC. Wrong port, and the aggregated API returns an
// error for every query while both pods report healthy — the failure shows up
// as an autoscaler that never acts.
func TestKedaPolicy_NamesTheGRPCPortTheChartRenders(t *testing.T) {
	t.Parallel()

	policies := manifests(t)[kedaPolicy]
	require.NotEmpty(t, policies)

	var ports []string

	for _, policy := range policies {
		for _, rule := range policy.Spec.Ingress {
			for _, block := range rule.ToPorts {
				for _, port := range block.Ports {
					ports = append(ports, port.Port)
				}
			}
		}

		for _, rule := range policy.Spec.Egress {
			for _, block := range rule.ToPorts {
				for _, port := range block.Ports {
					ports = append(ports, port.Port)
				}
			}
		}
	}

	// 9666 is the keda-operator Service's `metricsservice` port, read off the
	// rendered chart rather than remembered.
	assert.ElementsMatch(t, []string{"9666", "9666"}, ports,
		"%s must name 9666 from both sides: the deny covers each direction separately",
		kedaPolicy)
}

// acmeSolverPolicy lets Traefik reach the pod that answers an HTTP-01
// challenge.
const acmeSolverPolicy = "49-allow-acme-solver.yaml"

// TestACMESolverPolicy_IsScopedToSolverPods pins both halves of the one rule
// whose flow the cluster cannot be relied on to exercise.
//
// 50-allow-acme.yaml lets cert-manager talk to Let's Encrypt; this is the
// other direction, and the gap between them is the whole HTTP-01 path.
// Let's Encrypt validates by fetching http://<domain>/.well-known/acme-challenge/…
// from the internet, which arrives at Traefik and has to reach a solver pod in
// the certificate's namespace — a flow the default deny drops, measured
// directly: before this policy a pod carrying the solver label was unreachable
// from Traefik, and with it the request succeeded.
//
// Both halves are asserted because widening either one is the change that
// looks like a fix. An empty selector in Cilium does not mean "solver pods",
// it means every endpoint, so a policy that kept its ingress rule and lost its
// selector would open Traefik's reach to the whole cluster with the file still
// reading as a narrow exception.
func TestACMESolverPolicy_IsScopedToSolverPods(t *testing.T) {
	t.Parallel()

	policies, found := manifests(t)[acmeSolverPolicy]
	require.True(t, found,
		"%s is gone: an HTTP-01 challenge under the deny has nothing to answer it",
		acmeSolverPolicy)
	require.Len(t, policies, 1)

	solver := policies[0]

	// The label is cert-manager's, and nothing compares a YAML string to it.
	// Cilium prefixes a Kubernetes label with its source, so the selector is
	// the constant with `k8s:` in front.
	assert.Equal(t, map[string]string{"k8s:" + platform.ACMESolverLabel: "true"},
		solver.Spec.EndpointSelector.MatchLabels,
		"%s selects %v. Anything other than the solver label either misses the pod — a "+
			"challenge that times out — or selects more than it should",
		acmeSolverPolicy, solver.Spec.EndpointSelector.MatchLabels)

	require.Len(t, solver.Spec.Ingress, 1)
	require.Len(t, solver.Spec.Ingress[0].FromEndpoints, 1,
		"%s permits more than one source. Only the ingress controller receives the "+
			"validation request; a second source here is a separate decision",
		acmeSolverPolicy)

	// Namespace and label both from the chart's own key: the release installs
	// into it and stamps app.kubernetes.io/name from it, so a rename reaches
	// here rather than leaving a selector that matches nothing.
	assert.Equal(t, map[string]string{
		"k8s:io.kubernetes.pod.namespace": charts.Traefik,
		"k8s:app.kubernetes.io/name":      charts.Traefik,
	}, solver.Spec.Ingress[0].FromEndpoints[0].MatchLabels,
		"%s no longer names the ingress controller as the source", acmeSolverPolicy)

	assert.Empty(t, solver.Spec.Egress,
		"%s has grown an egress rule. A solver pod is an HTTP server that is asked for a "+
			"file; it initiates nothing", acmeSolverPolicy)
}
