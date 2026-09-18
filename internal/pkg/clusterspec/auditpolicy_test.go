package clusterspec

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sigs.k8s.io/yaml"
)

// TestAuditPolicy_TheCommittedOnePasses is the first thing that would break if
// the file were edited into something the API server refuses.
//
// It matters more than it looks: a policy the API server cannot parse stops
// the static pod from starting, and the cluster then settles with etcd and
// kubelet healthy and 6443 refusing connections — the same shape the
// cloud-provider flag produced, and just as hard to read back to its cause.
func TestAuditPolicy_TheCommittedOnePasses(t *testing.T) {
	t.Parallel()

	policy, err := AuditPolicy()
	require.NoError(t, err)

	assert.Equal(t, AuditPolicyAPIVersion, policy["apiVersion"])
	assert.Equal(t, AuditPolicyKind, policy["kind"])

	// Dropped at the source rather than in whatever reads the log: the stage
	// carries no outcome, so every request would otherwise appear twice with
	// only the second one saying whether it was allowed.
	assert.Equal(t, []any{"RequestReceived"}, policy["omitStages"])

	rules, ok := policy["rules"].([]any)
	require.True(t, ok)
	assert.NotEmpty(t, rules)
}

// TestAuditPolicy_SecretsAreACeilingAndAFloor holds the placement the file
// argues for, which is the one thing about this policy that is not obvious
// from reading any single rule.
//
// A CEILING: no rule may record these above Metadata, because the level above
// it writes the request body and for a Secret the body is the secret — a log
// kept for thirty days would become the least protected copy of every
// credential in the cluster.
//
// A FLOOR: the rule dropping kubelet reads as noise is correct for pods and
// endpoints and wrong for these. Kubelet credentials are node-scoped exactly
// so a compromised node reaches only its own secrets, and the audit log is
// what would show it reaching further — so the secrets rule has to come
// FIRST, before anything that could drop those reads.
func TestAuditPolicy_SecretsAreACeilingAndAFloor(t *testing.T) {
	t.Parallel()

	raw, err := auditPolicyFS.ReadFile(auditPolicyFile)
	require.NoError(t, err)

	var policy auditPolicy
	require.NoError(t, yaml.UnmarshalStrict(raw, &policy))

	secrets := -1
	nodes := -1

	for i, rule := range policy.Rules {
		if secrets < 0 && rule.mentions(SecretResources) {
			secrets = i

			assert.Equal(t, "Metadata", rule.Level)
		}

		if nodes < 0 && slices.Contains(rule.UserGroups, "system:nodes") {
			nodes = i
		}
	}

	require.GreaterOrEqual(t, secrets, 0, "no rule names %v", SecretResources)
	require.GreaterOrEqual(t, nodes, 0, "no rule drops kubelet reads, so the floor below is moot")

	assert.Less(t, secrets, nodes,
		"the rule naming %v comes after the one dropping system:nodes reads, so a compromised "+
			"node reading somebody else's secret is not recorded", SecretResources)
}

// TestCheckAuditPolicy_RejectsWhatItClaims proves each check by breaking the
// policy in the way it exists to catch.
//
// The first case is the one worth the whole file being typed: `userGroup` for
// `userGroups` is not an error to Talos, which passes the policy through
// unstructured, nor to the API server, which ignores what it does not
// recognise. The rule just stops selecting anybody, and nothing says so until
// somebody looks for an event that was never written.
func TestCheckAuditPolicy_RejectsWhatItClaims(t *testing.T) {
	t.Parallel()

	const head = "apiVersion: audit.k8s.io/v1\nkind: Policy\nrules:\n"

	for name, tc := range map[string]struct {
		policy string
		says   string
	}{
		"a misspelt field": {
			policy: head + "  - level: None\n    userGroup: [system:nodes]\n  - level: Metadata\n",
			says:   "unknown field",
		},
		"a level that does not exist": {
			policy: head + "  - level: Everything\n",
			says:   "which is not one of",
		},
		"a stage that does not exist": {
			policy: "apiVersion: audit.k8s.io/v1\nkind: Policy\nomitStages: [Received]\nrules:\n  - level: Metadata\n",
			says:   "omits stage",
		},
		"secrets above Metadata": {
			policy: head + "  - level: RequestResponse\n    resources:\n      - group: \"\"\n        resources: [secrets]\n  - level: Metadata\n",
			says:   "the body is the credential",
		},
		"a None rule that selects nothing": {
			policy: head + "  - level: None\n  - level: Metadata\n",
			says:   "selects nothing and is not the last rule",
		},
		"a last rule that selects something": {
			policy: head + "  - level: Metadata\n    verbs: [get]\n",
			says:   "rather than everything",
		},
		"the wrong kind": {
			policy: "apiVersion: audit.k8s.io/v1\nkind: AuditPolicy\nrules:\n  - level: Metadata\n",
			says:   "the API server reads",
		},
		"no rules at all": {
			policy: "apiVersion: audit.k8s.io/v1\nkind: Policy\nrules: []\n",
			says:   "records nothing at all",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := checkAuditPolicy([]byte(tc.policy))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.says)
		})
	}
}

// TestCheckAuditPolicy_AcceptsAPlainPolicy is the other half: a check that
// refuses a valid policy is worse than none, because the remedy is to delete
// it.
func TestCheckAuditPolicy_AcceptsAPlainPolicy(t *testing.T) {
	t.Parallel()

	assert.NoError(t, checkAuditPolicy([]byte(
		"apiVersion: audit.k8s.io/v1\nkind: Policy\nomitStages: [RequestReceived]\nrules:\n"+
			"  - level: None\n    nonResourceURLs: [/healthz*]\n"+
			"  - level: Metadata\n")))
}
