package charts

import (
	"strconv"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"
)

// PolicyController is Sigstore's admission controller: it checks an image's
// signature, or its digest, before a pod that runs it is admitted.
//
// Installed with nothing to enforce. Its webhooks match only namespaces
// labelled policy.sigstore.dev/include=true, and nothing carries that label
// until the policies generated from internal/pkg/imagepolicy are in place —
// so on its own this chart admits everything it is asked about.
//
// The chart lags the application: 0.10.8 ships v0.13.1 while upstream has
// released v0.15.1. Nothing between them changes the CRDs or the webhook's
// arguments, but the image is the chart's own, held by the appversions check,
// and moves when the chart does.
// PolicyController is the registry key, and what a layer names when it
// installs this chart. Exported because a layer writing the key as a literal is
// the drift this package exists to remove.
const PolicyController = "policy-controller"

// PolicyControllerNamespace is where the chart runs. Sigstore's own spelling,
// which its documentation and the cleanup Job's examples assume.
const PolicyControllerNamespace = "cosign-system"

// PolicyControllerReplicas is how many webhook pods run.
//
// Two, because the chart's budget keeps one available: with a single replica
// that budget can never be met during a drain, and `task cluster:upgrade:talos`
// waits out its whole drain timeout on the node that holds it.
const PolicyControllerReplicas = 2

// The values keys this file's settings write, in the chart's own spelling.
const (
	policyControllerReplicas = "webhook.replicaCount"
	policyControllerPriority = "webhook.priorityClass"
)

func init() {
	register(Definition{
		Key:   PolicyController,
		Layer: platform.LayerClusterServices,
		Chart: Chart{
			Name:       "policy-controller",
			Repo:       "https://sigstore.github.io/helm-charts",
			Version:    "0.10.8", // app 0.13.1
			AppVersion: "0.13.1",
			Namespace:  PolicyControllerNamespace,
		},
		Workloads: []Object{
			{Kind: Deployment, Name: "policy-controller-webhook"},
		},
		Settings: []Setting{
			{
				Set:    []string{policyControllerReplicas + "=" + strconv.Itoa(PolicyControllerReplicas)},
				Expect: "replicas: " + strconv.Itoa(PolicyControllerReplicas),
				Why: "the chart's budget keeps one webhook available, so a single replica " +
					"blocks every drain of the node it runs on",
			},
			{
				Set:    []string{policyControllerPriority + "=" + PriorityClusterCritical},
				Expect: PriorityLine(PriorityClusterCritical),
				Why: "once a namespace is enforced the webhook is in the admission path for " +
					"its pods, and evicting it stops them being scheduled",
			},
		},
	})
}
