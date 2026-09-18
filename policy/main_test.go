package main

import (
	"context"
	"strconv"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/policyx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
)

// rule builds the property shape hcloud gives a firewall rule, so the cases
// below read as the rules they represent rather than as map literals.
func rule(port string, sources ...string) property.Map {
	values := make([]property.Value, 0, len(sources))
	for _, source := range sources {
		values = append(values, property.New(source))
	}

	return property.NewMap(map[string]property.Value{
		"port":      property.New(port),
		"sourceIps": property.New(property.NewArray(values)),
	})
}

func TestWorldOpenAdminPorts_ReportsTheRuleThisPackExistsFor(t *testing.T) {
	t.Parallel()

	// The measured hole: BuildFirewallRules accepts an Extra rule opening
	// tcp/6443 to the world, because its validate() checks that the source
	// list is non-empty and never what the sources are.
	violations := worldOpenAdminPorts(rule("6443", worldCIDR))

	require.Len(t, violations, 1)
	assert.Contains(t, violations[0], "6443")
	assert.Contains(t, violations[0], "the Kubernetes API")
	assert.Contains(t, violations[0], "network.adminCIDRs")
}

func TestWorldOpenAdminPorts_CoversEveryAdminPortAndBothWorldSpellings(t *testing.T) {
	t.Parallel()

	// Both ports, because 50000 is the worse one — the Talos API applies
	// machine configuration and resets nodes — and both spellings of "the
	// whole internet", because a rule can be opened to either and only the
	// IPv4 one is the obvious mistake.
	for port := range adminPorts {
		for _, world := range []string{worldCIDR, worldCIDRIPv6} {
			violations := worldOpenAdminPorts(rule(port, world))
			assert.Len(t, violations, 1, "port %s open to %s went unreported", port, world)
		}
	}
}

func TestWorldOpenAdminPorts_ReportsEachWorldOpenSourceSeparately(t *testing.T) {
	t.Parallel()

	// A rule can carry an operator CIDR and a world CIDR at once — which is
	// how this arrives in practice, as an Extra rule appended beside the
	// baseline. The operator CIDR must not suppress the report.
	violations := worldOpenAdminPorts(rule("6443", "203.0.113.4/32", worldCIDR, worldCIDRIPv6))

	assert.Len(t, violations, 2)
}

func TestWorldOpenAdminPorts_SaysNothingAboutRulesThatAreFine(t *testing.T) {
	t.Parallel()

	for name, subject := range map[string]property.Map{
		// The baseline rules internal/pkg/clusterspec builds, which must never be flagged:
		// a false positive here blocks every apply.
		"kube-apiserver from an operator CIDR": rule("6443", "203.0.113.4/32"),
		"talos api from an operator CIDR":      rule("50000", "203.0.113.4/32"),
		// A world-open port that is not an admin port is a different decision
		// — ingress on 80 and 443 is world-open on purpose — and not this
		// policy's business.
		"http from the world":  rule("80", worldCIDR),
		"https from the world": rule("443", worldCIDR),
		// Shapes the policy must survive rather than panic on.
		"no port":        property.NewMap(map[string]property.Value{"sourceIps": property.New(property.NewArray(nil))}),
		"no sources":     property.NewMap(map[string]property.Value{"port": property.New("6443")}),
		"port not a str": property.NewMap(map[string]property.Value{"port": property.New(6443.0)}),
		"empty":          property.NewMap(nil),
	} {
		assert.Empty(t, worldOpenAdminPorts(subject), name)
	}
}

func TestAdminPorts_AreThePortsTheFirewallActuallyOpens(t *testing.T) {
	t.Parallel()

	// Read from internal/pkg/clusterspec rather than restated. The first version of this
	// test compared adminPorts against the literals "6443" and "50000", which
	// asserted nothing at all: both sides were hand-written here, so the
	// cluster package could move a port and this would still pass.
	//
	// The import is test-only, which is why it does not contradict the note on
	// adminPorts: _test.go files are not in the plugin binary, so the policy
	// plugin still carries no provider SDK.
	want := map[string]bool{
		strconv.Itoa(clusterspec.PortKubeAPI):   true,
		strconv.Itoa(clusterspec.PortTalosdAPI): true,
	}

	got := map[string]bool{}
	for port := range adminPorts {
		got[port] = true
	}

	assert.Equal(t, want, got,
		"the policy's admin ports and the ports internal/pkg/clusterspec opens have diverged")
}

// recorder is a PolicyManager that keeps what a policy reported, so the real
// ValidateResource closures can be driven without running a policy plugin.
type recorder struct {
	violations []string
	urns       []string
}

func (r *recorder) ReportViolation(message, urn string) {
	r.violations = append(r.violations, message)
	r.urns = append(r.urns, urn)
}

// validate runs a policy against one resource and returns what it reported.
func validate(t *testing.T, policy policyx.ResourceValidationPolicy, resource policyx.AnalyzerResource) *recorder {
	t.Helper()

	manager := &recorder{}

	require.NoError(t, policy.Validate(context.Background(), policyx.ResourceValidationArgs{
		Manager:  manager,
		Resource: resource,
		DryRun:   true,
	}))

	return manager
}

// firewall is an hcloud firewall resource carrying the given rules.
func firewall(rules ...property.Map) policyx.AnalyzerResource {
	values := make([]property.Value, 0, len(rules))
	for _, one := range rules {
		values = append(values, property.New(one))
	}

	return policyx.AnalyzerResource{
		Type: typeHcloudFirewall,
		URN:  "urn:pulumi:dev::hetzner-cluster::hcloud:index/firewall:Firewall::probe",
		Properties: property.NewMap(map[string]property.Value{
			"rules": property.New(property.NewArray(values)),
		}),
	}
}

func TestFirewallPolicy_ReportsAWorldOpenAdminPort(t *testing.T) {
	t.Parallel()

	// End-to-end through the policy the pack registers: the type guard, the
	// walk over `rules`, the judgement and the report. Proving this here is
	// what a green `task policy:cluster` cannot prove — a policy that matched
	// nothing would also pass.
	got := validate(t, firewallAdminPortsNotWorldOpen(),
		firewall(rule("50000", worldCIDR)))

	require.Len(t, got.violations, 1)
	assert.Contains(t, got.violations[0], "50000")
	assert.Contains(t, got.violations[0], "remote node reset")
	assert.Contains(t, got.urns[0], "Firewall::probe", "the violation names no resource")
}

func TestFirewallPolicy_PassesTheBaselineRuleSet(t *testing.T) {
	t.Parallel()

	// The rules internal/pkg/clusterspec actually builds, from the real builder. A false
	// positive here would block every apply, so this is the more important
	// half of the policy.
	baseline, err := clusterspec.BuildFirewallRules([]string{"203.0.113.4/32"},
		clusterspec.FirewallRuleOptions{AllowICMP: true})
	require.NoError(t, err)

	as := make([]property.Map, 0, len(baseline))
	for _, one := range baseline {
		as = append(as, rule(one.Port, one.SourceIPs...))
	}

	assert.Empty(t, validate(t, firewallAdminPortsNotWorldOpen(), firewall(as...)).violations)
}

func TestFirewallPolicy_LooksAtNothingButFirewalls(t *testing.T) {
	t.Parallel()

	// A resource of another type must be passed over rather than inspected:
	// `port` and `sourceIps` are generic enough to appear elsewhere.
	other := policyx.AnalyzerResource{
		Type: typeHcloudServer,
		Properties: property.NewMap(map[string]property.Value{
			"rules": property.New(property.NewArray([]property.Value{
				property.New(rule("6443", worldCIDR)),
			})),
		}),
	}

	assert.Empty(t, validate(t, firewallAdminPortsNotWorldOpen(), other).violations)
}

func TestServerPolicy_ReportsAServerOffThePrivateNetwork(t *testing.T) {
	t.Parallel()

	server := func(networks ...property.Value) policyx.AnalyzerResource {
		return policyx.AnalyzerResource{
			Type: typeHcloudServer,
			URN:  "urn:pulumi:dev::hetzner-cluster::hcloud:index/server:Server::cp-0",
			Properties: property.NewMap(map[string]property.Value{
				"networks": property.New(property.NewArray(networks)),
			}),
		}
	}

	// No attachment: etcd and kubelet would ride the public interface, where
	// the firewall filters peer traffic — which is the failure that cost a
	// quorum when the control plane first went to three members.
	got := validate(t, serverJoinsPrivateNetwork(), server())
	require.Len(t, got.violations, 1)
	assert.Contains(t, got.violations[0], "network.nodeSubnet")

	// Attached, which is what internal/pkg/clusterspec builds.
	attached := property.New(property.NewMap(map[string]property.Value{
		"networkId": property.New(1.0),
		"ip":        property.New("10.0.1.2"),
	}))
	assert.Empty(t, validate(t, serverJoinsPrivateNetwork(), server(attached)).violations)

	// And a server resource is the only thing it judges.
	assert.Empty(t, validate(t, serverJoinsPrivateNetwork(),
		policyx.AnalyzerResource{Type: typeHelmRelease}).violations)
}

func TestHelmPolicy_ReportsAReleaseWithNoPinnedVersion(t *testing.T) {
	t.Parallel()

	release := func(version string) policyx.AnalyzerResource {
		return policyx.AnalyzerResource{
			Type: typeHelmRelease,
			URN:  "urn:pulumi:dev::platform::kubernetes:helm.sh/v3:Release::cilium",
			Properties: property.NewMap(map[string]property.Value{
				"version": property.New(version),
			}),
		}
	}

	for _, unpinned := range []string{"", "   "} {
		got := validate(t, helmReleasePinsVersion(), release(unpinned))
		require.Len(t, got.violations, 1, "version %q went unreported", unpinned)
		assert.Contains(t, got.violations[0], "internal/pkg/charts")
	}

	// A pinned version, in the two spellings internal/pkg/charts accepts.
	for _, pinned := range []string{"1.20.1", "v1.21.2"} {
		assert.Empty(t, validate(t, helmReleasePinsVersion(), release(pinned)).violations, pinned)
	}

	// A release with no `version` property at all — a shape the policy must
	// survive rather than panic on, and still report.
	bare := policyx.AnalyzerResource{Type: typeHelmRelease, Properties: property.NewMap(nil)}
	assert.Len(t, validate(t, helmReleasePinsVersion(), bare).violations, 1)
}

func TestStringOf_AndArrayOf_SurviveEveryShapeAResourceCanHave(t *testing.T) {
	t.Parallel()

	// These run inside a policy plugin, where a panic fails the entire preview
	// with a stack trace instead of a verdict. Every wrong-type and missing
	// case has to return a zero value.
	subject := property.NewMap(map[string]property.Value{
		"text":   property.New("value"),
		"number": property.New(1.0),
		"list":   property.New(property.NewArray([]property.Value{property.New("one")})),
	})

	assert.Equal(t, "value", stringOf(subject, "text"))
	assert.Empty(t, stringOf(subject, "number"), "a number read as a string")
	assert.Empty(t, stringOf(subject, "absent"))
	assert.Empty(t, stringOf(property.NewMap(nil), "text"))

	assert.Len(t, arrayOf(subject, "list"), 1)
	assert.Empty(t, arrayOf(subject, "text"), "a string read as an array")
	assert.Empty(t, arrayOf(subject, "absent"))
}

func TestPolicyPack_BuildsWithEveryPolicyRegistered(t *testing.T) {
	t.Parallel()

	// The pack is what Pulumi loads; a policy left out of the slice is a check
	// that silently never runs. Building it here also catches a duplicate or
	// malformed name, which the SDK rejects at construction.
	pack, err := newPolicyPack(nil)
	require.NoError(t, err)
	require.NotNil(t, pack)

	names := map[string]bool{}
	for _, policy := range pack.Policies() {
		names[policy.Name()] = true
	}

	assert.Equal(t, map[string]bool{
		"hcloud-admin-ports-not-world-open":   true,
		"hcloud-server-joins-private-network": true,
		"helm-release-pins-chart-version":     true,
	}, names)
}
