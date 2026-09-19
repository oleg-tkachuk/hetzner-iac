package charts

import "github.com/oleg-tkachuk/hetzner-iac/internal/pkg/platform"

// ArgoCD reconciles workloads from git, and is installed and reconciling
// nothing until `gitops:repoURL` names a repository.
//
// The chart's version and its application's are far apart — chart 10.9.2 ships
// app v3.5.3 — which is the clearest case in this registry for pinning both:
// a reader should not have to resolve a chart to know what runs.
//
// Three objects, and the StatefulSet among them is the application controller:
// it holds the reconciliation state, which is why it is not a Deployment.
// ArgoCD is the registry key, and what a layer names when it installs this
// chart. Exported because a layer writing the key as a literal is the drift
// this package exists to remove.
const ArgoCD = "argo-cd"

func init() {
	register(Definition{
		Key:   ArgoCD,
		Layer: platform.LayerGitOps,
		Chart: Chart{
			Name:       "argo-cd",
			Repo:       "https://argoproj.github.io/argo-helm",
			Version:    "10.9.2", // app v3.5.3
			AppVersion: "v3.5.3",
			Namespace:  "argocd",
		},
		Workloads: []Object{
			{Kind: Deployment, Name: "argo-cd-argocd-server"},
			{Kind: Deployment, Name: "argo-cd-argocd-repo-server"},
			{Kind: StatefulSet, Name: "argo-cd-argocd-application-controller"},
		},
	})
}
