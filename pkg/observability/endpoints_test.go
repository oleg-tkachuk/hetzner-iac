package observability_test

import (
	"strings"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/observability"
	"github.com/stretchr/testify/assert"
)

func TestEndpoints_MatchTheServicesTheChartsCreate(t *testing.T) {
	t.Parallel()

	// Verified by rendering the charts offline (`task charts:render-check`):
	//
	//   loki-gateway   Service, port 80    -> no port in the URL
	//   tempo          Service, port 3200  -> the chart exposes NO 3100
	//
	// Pointing Grafana at 3100 does not fail the apply — the platform comes up
	// and traces simply never load, which is the expensive kind of wrong.
	assert.Equal(t, "http://loki-gateway.observability.svc.cluster.local", observability.LokiGateway)
	assert.Equal(t, "http://tempo.observability.svc.cluster.local:3200", observability.TempoHTTP)
	assert.NotContains(t, observability.TempoHTTP, ":3100", "Tempo's Service has no 3100")
}

func TestAlloyConfig_WritesToTheGatewayItIsGiven(t *testing.T) {
	t.Parallel()

	// Interpolated rather than written twice: the address Alloy pushes to and
	// the one Grafana reads from cannot drift apart.
	config := observability.AlloyConfig()

	assert.Contains(t, config, observability.LokiGateway+"/loki/api/v1/push")
	assert.NotContains(t, config, "%s", "the template must be fully interpolated")
}

func TestAlloyConfig_ReferencesOnlyDeclaredComponents(t *testing.T) {
	t.Parallel()

	// Alloy refuses to start on a reference to a component that is not
	// declared, and the failure is a crash-loop rather than an error at apply.
	// `task observability:check` proves this against the real binary; this
	// pins it without Docker.
	config := observability.AlloyConfig()

	for _, reference := range []string{
		"discovery.kubernetes.pods.targets",
		"discovery.relabel.pods.output",
		"loki.write.default.receiver",
	} {
		assert.Contains(t, config, reference)
	}

	for _, declaration := range []string{
		`discovery.kubernetes "pods"`,
		`discovery.relabel "pods"`,
		`loki.source.kubernetes "pods"`,
		`loki.write "default"`,
	} {
		assert.Equal(t, 1, strings.Count(config, declaration),
			"component %q must be declared exactly once", declaration)
	}
}

func TestAlloyConfig_CollectsThroughTheAPI(t *testing.T) {
	t.Parallel()

	// loki.source.kubernetes reads container logs from the Kubernetes API,
	// which is why the DaemonSet needs no host mount — and a host mount is
	// what puts it outside the Pod Security baseline Talos enforces
	// everywhere but kube-system.
	//
	// Switching to a file-based source would reinstate that need, so this is
	// the test that has to fail first: the mount question comes back with it.
	config := observability.AlloyConfig()

	assert.Contains(t, config, "loki.source.kubernetes")
	assert.NotContains(t, config, "loki.source.file",
		"a file source reads the node filesystem, which needs a hostPath the DaemonSet cannot have")
	assert.NotContains(t, config, "local.file_match",
		"discovering log files on the node needs a hostPath the DaemonSet cannot have")
}

func TestAlloyConfig_LabelsLogsWithKubernetesMetadata(t *testing.T) {
	t.Parallel()

	// Without these relabel rules the logs arrive in Loki with no namespace,
	// pod or container label — collected, stored, and unqueryable.
	config := observability.AlloyConfig()

	for _, label := range []string{"namespace", "pod", "container", "job"} {
		assert.Contains(t, config, `target_label  = "`+label+`"`)
	}
}

func TestAlloyConfig_IsStable(t *testing.T) {
	t.Parallel()

	// A config that differs between calls would make the Helm release show a
	// diff on every run and restart the collector for nothing.
	assert.Equal(t, observability.AlloyConfig(), observability.AlloyConfig())
}
