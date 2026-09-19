package values_test

import (
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/charts"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterref"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/values"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func TestNames_AreAllPinnedCharts(t *testing.T) {
	t.Parallel()

	// A template named after nothing in the registry is a file nobody reads:
	// the layer asks for values by the registry key, so a misnamed file
	// silently leaves the chart on its defaults.
	names, err := values.Names()
	require.NoError(t, err)
	require.NotEmpty(t, names, "no values templates found — the embed pattern must be wrong")

	for _, name := range names {
		_, err := charts.Get(name)
		assert.NoError(t, err, "values template %q names no pinned chart", name)
	}
}

func TestSource_RefusesAnUnknownChart(t *testing.T) {
	t.Parallel()

	_, err := values.Source("no-such-chart")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no values template")
}

func TestRender_RefusesAFieldTheDataDoesNotHave(t *testing.T) {
	t.Parallel()

	// The property that makes a template safe to edit. Without it a renamed
	// field renders as the empty string and the chart runs on a default
	// nobody chose — the same silent class as a misspelt Helm key.
	names, err := values.Names()
	require.NoError(t, err)

	_, err = values.Render(names[0], struct{ Nothing string }{})
	require.Error(t, err, "a template rendered against the wrong data must fail")
}

func TestTemplates_RenderValidYAMLFromProbeData(t *testing.T) {
	t.Parallel()

	// Every template, rendered the way `charts:render-check` renders it.
	// A template that only parses as YAML when a value happens to be non-empty
	// is a template that breaks on an empty config key — and one whose action
	// supplies an indented block, as Alloy's collector config does, cannot be
	// checked any other way.
	names, err := values.Names()
	require.NoError(t, err)

	for _, name := range names {
		probe, err := values.Probe(name)
		require.NoError(t, err, "every template needs probe data, or the render check cannot render it")

		text, err := values.Render(name, probe)
		require.NoError(t, err)

		var out map[string]any
		assert.NoError(t, yaml.Unmarshal([]byte(text), &out), "%s does not render valid yaml", name)
		assert.NotEmpty(t, out, "%s renders nothing", name)
	}
}

func TestProbe_RefusesAnUnknownChart(t *testing.T) {
	t.Parallel()

	// nil would render a template whose every field resolves to nothing —
	// a values file of empty strings, and a check that passes on it.
	_, err := values.Probe("no-such-chart")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no probe data")
}

func TestTemplates_MentionEverySettingThatFailsSilently(t *testing.T) {
	t.Parallel()

	// The guarantee the move to templates could have lost. A Helm key in a
	// YAML file is not a Go identifier, so nothing compiles it — but
	// internal/pkg/charts holds the keys whose misspelling leaves a default in
	// place with nothing said, and this asserts each one is still spelled the
	// way the render check will look for it.
	traefik, err := values.Source("traefik")
	require.NoError(t, err)

	for _, key := range []string{
		charts.TraefikPorts,
		charts.TraefikEntryPointWeb,
		charts.TraefikEntryPointTLS,
		charts.TraefikProxyProtocol,
		charts.TraefikTrustedIPs,
	} {
		assert.Contains(t, traefik, key,
			"the traefik template no longer spells %q the way the render check asserts it", key)
	}

	// The render check proves the CHART honours this key; it renders with its
	// own --set and would pass whether or not any template sets it. This is
	// the other half: that the template actually does.
	csi, err := values.Source("hcloud-csi")
	require.NoError(t, err)

	assert.Contains(t, csi, charts.HcloudCSIDefaultLocation,
		"the hcloud-csi template no longer sets %q, so the controller is back to discovering "+
			"its location at startup — the CrashLoopBackOff this was written for",
		charts.HcloudCSIDefaultLocation)
}

// TestTemplates_SetThePriorityClassEachComponentNeeds is the same half again,
// for a setting whose absence is invisible until a node runs out of memory.
//
// The render check proves each chart honours the key. It renders with its own
// `--set`, so it passes whether or not a template sets anything — and an
// unset priority is not a failure, it is a pod ranked beside the workloads it
// serves. Nothing else would notice.
//
// Argo CD is deliberately absent, and asserted absent: it reconciles rather
// than serves, so marking it cluster-critical would let it outrank the
// workloads under exactly the pressure where they matter more.
func TestTemplates_SetThePriorityClassEachComponentNeeds(t *testing.T) {
	t.Parallel()

	for chart, want := range map[string][]string{
		"traefik":      {charts.PriorityClusterCritical},
		"cert-manager": {charts.PriorityClusterCritical},
		"hcloud-ccm":   {charts.PriorityClusterCritical},
		// Two, and not the same one: the node plugin is a DaemonSet.
		"hcloud-csi": {charts.PriorityClusterCritical, charts.PriorityNodeCritical},
	} {
		source, err := values.Source(chart)
		require.NoError(t, err, chart)

		assert.Contains(t, source, charts.PriorityClassName,
			"the %s template no longer spells %q the way the render check asserts it",
			chart, charts.PriorityClassName)

		for _, class := range want {
			assert.Contains(t, source, class,
				"the %s template no longer asks for %s, so the kubelet ranks it beside the "+
					"workloads it serves", chart, class)
		}
	}

	argo, err := values.Source("argo-cd")
	require.NoError(t, err)

	assert.NotContains(t, argo, charts.PriorityClassName,
		"argo-cd has been given a priority class. It reconciles rather than serves, so this "+
			"lets it outrank workloads under memory pressure — if that is intended, say why "+
			"in internal/pkg/charts and change this")
}

// TestHcloudCSI_LocationIsRenderedNotLeftEmpty is the value's own failure mode:
// an empty string is valid YAML and a valid chart value, and it puts the
// controller straight back on the discovery path.
func TestHcloudCSI_LocationIsRenderedNotLeftEmpty(t *testing.T) {
	t.Parallel()

	rendered, err := values.Render("hcloud-csi", values.HcloudCSI{Location: "fsn1"})
	require.NoError(t, err)

	assert.Contains(t, rendered, charts.HcloudCSIDefaultLocation+": fsn1")
	assert.NotContains(t, rendered, charts.HcloudCSIDefaultLocation+": \n",
		"an empty location renders the key with no value, which the chart reads as unset")
}

func TestArgoCD_AnUnsetDomainStaysAnEmptyString(t *testing.T) {
	t.Parallel()

	// The regression this exists for, and it cost seventeen minutes of a
	// deploy before the release rolled back.
	//
	// `domain: {{ .Domain }}` renders as YAML null when the domain is unset,
	// and this chart interpolates null straight into argocd-cm — measured
	// against the chart itself:
	//
	//     domain: ""   ->  url: https://
	//     domain:      ->  url: https://%!s(<nil>)
	//
	// The server then runs with a nonsense URL, never becomes available, and
	// Helm waits out its whole timeout with the cause nowhere in the output.
	rendered, err := values.Render("argo-cd", values.ArgoCD{
		Domain: "", IngressClass: "traefik", Issuer: "letsencrypt", Replicas: 2,
	})
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(rendered), &out))

	global, ok := out["global"].(map[string]any)
	require.True(t, ok)

	domain, present := global["domain"]
	require.True(t, present, "global.domain must be set, not absent")
	assert.Equal(t, "", domain,
		"an unset domain must reach the chart as an empty string, never as null")
}

// TestProbeData_CarriesThePinnedValuesRatherThanACopy separates the two kinds
// of field in the probe map.
//
// Most of it is deliberately fake — documentation CIDRs, example.com — because
// the render check only needs the keys to reach the chart's output. Four
// fields are not: they are the platform's own pinned values, and the check
// asserts those numbers appear in what the chart renders. A copy of one here
// keeps a passing check pointed at a value the cluster no longer uses.
func TestProbeData_CarriesThePinnedValuesRatherThanACopy(t *testing.T) {
	t.Parallel()

	cilium, err := values.Probe("cilium")
	require.NoError(t, err)

	cni, ok := cilium.(values.Cilium)
	require.True(t, ok, "the cilium probe is %T", cilium)

	assert.Equal(t, clusterspec.KubePrismPort, cni.APIPort,
		"the cilium probe renders port %d while Talos listens on %d",
		cni.APIPort, clusterspec.KubePrismPort)

	traefik, err := values.Probe("traefik")
	require.NoError(t, err)

	ingress, ok := traefik.(values.Traefik)
	require.True(t, ok, "the traefik probe is %T", traefik)

	assert.Equal(t, platform.IngressNodePortHTTP, ingress.NodePortHTTP)
	assert.Equal(t, platform.IngressNodePortHTTPS, ingress.NodePortHTTPS)

	csi, err := values.Probe("hcloud-csi")
	require.NoError(t, err)

	storage, ok := csi.(values.HcloudCSI)
	require.True(t, ok, "the hcloud-csi probe is %T", csi)

	assert.Equal(t, clusterref.ProbeLocation, storage.Location)

	// argo-cd carries two, and only one of them read its constant: the issuer
	// name was the literal "letsencrypt" while IngressClass beside it was
	// already platform's. This test covered cilium, traefik and hcloud-csi
	// and not this chart, which is how the literal survived.
	argocd, err := values.Probe("argo-cd")
	require.NoError(t, err)

	gitops, ok := argocd.(values.ArgoCD)
	require.True(t, ok, "the argo-cd probe is %T", argocd)

	assert.Equal(t, platform.IngressClass, gitops.IngressClass)
	assert.Equal(t, platform.IssuerName, gitops.Issuer,
		"the argo-cd probe renders issuer %q while the platform creates %q",
		gitops.Issuer, platform.IssuerName)
}
