package charts

import "github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"

// Keda is event-driven autoscaling, and the only optional chart here: the
// layer installs it when `kedaEnabled` is set, which is why its workloads are
// Optional — a cluster without it is not a cluster missing something.
//
// The pin exists either way. A chart nobody pins is a chart whose version is
// whatever the day decides.
//
// What it cannot do is add nodes: the worker pools are pinned in the committed
// topology, so scaling past their capacity leaves pods Pending. The value is
// scale-to-zero and bursts inside capacity already paid for.
// Keda is the registry key, and what a layer names when it installs this
// chart. Exported because a layer writing the key as a literal is the drift
// this package exists to remove.
const Keda = "keda"

func init() {
	register(Definition{
		Key:   Keda,
		Layer: platform.LayerClusterServices,
		Chart: Chart{
			Name:       "keda",
			Repo:       "https://kedacore.github.io/charts",
			Version:    "2.20.2", // app 2.20.2
			AppVersion: "2.20.2",
			Namespace:  Keda,
		},
		Workloads: []Object{
			{Kind: Deployment, Name: "keda-operator", Optional: true},
			{Kind: Deployment, Name: "keda-operator-metrics-apiserver", Optional: true},
			{Kind: Deployment, Name: "keda-admission-webhooks", Optional: true},
		},
		Settings: []Setting{
			{
				Set:    []string{PriorityClassName + "=" + PriorityClusterCritical},
				Expect: PriorityLineQuoted(PriorityClusterCritical),
				Why: "the metrics API server is an aggregated API: evicted beside the workloads " +
					"it scales, it takes external.metrics.k8s.io down with it and every " +
					"autoscaler reading one fails on discovery rather than on a metric",
			},
		},
	})
}
