package hetzner

import (
	"embed"
	"fmt"
	"slices"

	"sigs.k8s.io/yaml"
)

// auditPolicyFile is the policy document, committed beside this file rather
// than assembled here.
//
// A Go map would be the shorter code and the worse artefact: an audit policy
// is read against Kubernetes' own examples, diffed when it changes, and
// argued about by whoever is deciding what to keep — all of which wants YAML
// with comments, not nested `map[string]any`. Go supplies nothing to it; it
// is static.
const auditPolicyFile = "auditpolicy.yaml"

//go:embed auditpolicy.yaml
var auditPolicyFS embed.FS

// Audit levels, in ascending order of what they record. The order is what
// makes "no higher than Metadata" a comparison rather than a list.
var auditLevels = []string{"None", "Metadata", "Request", "RequestResponse"}

// auditStages are the points an event can be emitted at, and the only values
// omitStages accepts.
var auditStages = []string{"RequestReceived", "ResponseStarted", "ResponseComplete", "Panic"}

// AuditPolicyAPIVersion and AuditPolicyKind are what the API server expects
// the document to declare.
const (
	AuditPolicyAPIVersion = "audit.k8s.io/v1"
	AuditPolicyKind       = "Policy"
)

// SecretResources are the ones whose request body is itself a credential, so
// no rule may record it. Named here because the check below is about them and
// a reader should not have to infer the list from a rule.
var SecretResources = []string{"secrets", "configmaps", "serviceaccounts/token"}

// auditPolicy is the shape this repository checks. It is not the full upstream
// schema: every field the policy actually uses, so that a strict decode fails
// on anything misspelt.
//
// Strict decoding is the whole reason it is typed at all. Talos passes
// auditPolicy through as an unstructured object and the API server ignores
// fields it does not recognise, so `userGroup` for `userGroups` is not an
// error anywhere — the rule simply stops selecting anybody, and the gap is
// invisible until somebody goes looking for an event that was never written.
type auditPolicy struct {
	APIVersion string      `json:"apiVersion"`
	Kind       string      `json:"kind"`
	OmitStages []string    `json:"omitStages,omitempty"`
	Rules      []auditRule `json:"rules"`
}

// auditRule is one rule of the policy.
type auditRule struct {
	Level           string          `json:"level"`
	Users           []string        `json:"users,omitempty"`
	UserGroups      []string        `json:"userGroups,omitempty"`
	Verbs           []string        `json:"verbs,omitempty"`
	Resources       []auditResource `json:"resources,omitempty"`
	Namespaces      []string        `json:"namespaces,omitempty"`
	NonResourceURLs []string        `json:"nonResourceURLs,omitempty"`
	OmitStages      []string        `json:"omitStages,omitempty"`
}

// auditResource is one resource selector inside a rule.
type auditResource struct {
	Group         string   `json:"group"`
	Resources     []string `json:"resources,omitempty"`
	ResourceNames []string `json:"resourceNames,omitempty"`
}

// selects reports whether the rule narrows what it applies to at all.
//
// A rule with no selector matches every event, which is correct for the
// catch-all at the bottom and a cluster-wide outage of the audit log anywhere
// above it: a `level: None` with nothing set drops everything after it too,
// because the first matching rule decides.
func (r auditRule) selects() bool {
	return len(r.Users) > 0 || len(r.UserGroups) > 0 || len(r.Verbs) > 0 ||
		len(r.Resources) > 0 || len(r.Namespaces) > 0 || len(r.NonResourceURLs) > 0
}

// mentions reports whether the rule selects any of the named resources.
func (r auditRule) mentions(names []string) bool {
	for _, resource := range r.Resources {
		for _, name := range resource.Resources {
			if slices.Contains(names, name) {
				return true
			}
		}
	}

	return false
}

// AuditPolicy is the policy as the machine config carries it, checked first.
//
// Returned as a map because that is what it is merged into — the patch is one
// document and this is a fragment of it. The typed decode happens on the way
// through, so the map handed to Talos is one that passed every check below.
func AuditPolicy() (map[string]any, error) {
	raw, err := auditPolicyFS.ReadFile(auditPolicyFile)
	if err != nil {
		return nil, fmt.Errorf("audit policy: %w", err)
	}

	if err := checkAuditPolicy(raw); err != nil {
		return nil, err
	}

	var policy map[string]any
	if err := yaml.Unmarshal(raw, &policy); err != nil {
		return nil, fmt.Errorf("audit policy: %w", err)
	}

	return policy, nil
}

// checkAuditPolicy holds every invariant the file's own comments claim.
//
// Separated from the read so a test can run it over a policy that is not the
// committed one — which is the only way to prove a check rejects what it says
// it rejects.
func checkAuditPolicy(raw []byte) error {
	var policy auditPolicy

	// Strict: the failure this exists for is a field name, not a value.
	if err := yaml.UnmarshalStrict(raw, &policy); err != nil {
		return fmt.Errorf("audit policy: %w", err)
	}

	if policy.APIVersion != AuditPolicyAPIVersion || policy.Kind != AuditPolicyKind {
		return fmt.Errorf("audit policy declares %s %s, and the API server reads %s %s",
			policy.APIVersion, policy.Kind, AuditPolicyAPIVersion, AuditPolicyKind)
	}

	if len(policy.Rules) == 0 {
		return fmt.Errorf("audit policy has no rules, which records nothing at all")
	}

	for _, stage := range policy.OmitStages {
		if !slices.Contains(auditStages, stage) {
			return fmt.Errorf("audit policy omits stage %q, which is not one of %v", stage, auditStages)
		}
	}

	secretCeiling := slices.Index(auditLevels, "Metadata")

	for i, rule := range policy.Rules {
		level := slices.Index(auditLevels, rule.Level)
		if level < 0 {
			return fmt.Errorf("audit policy rule %d has level %q, which is not one of %v",
				i, rule.Level, auditLevels)
		}

		for _, stage := range rule.OmitStages {
			if !slices.Contains(auditStages, stage) {
				return fmt.Errorf("audit policy rule %d omits stage %q, which is not one of %v",
					i, stage, auditStages)
			}
		}

		if rule.mentions(SecretResources) && level > secretCeiling {
			return fmt.Errorf("audit policy rule %d records %v at %s: the level above Metadata "+
				"writes the request body, and for these the body is the credential",
				i, SecretResources, rule.Level)
		}

		last := i == len(policy.Rules)-1

		if !rule.selects() && !last {
			return fmt.Errorf("audit policy rule %d selects nothing and is not the last rule, "+
				"so it decides every event after it and the rules below it are dead",
				i)
		}

		if last && rule.selects() {
			return fmt.Errorf("audit policy's last rule selects %s rather than everything, "+
				"so an event matching no rule is recorded at no level and is lost", rule.Level)
		}
	}

	return nil
}
