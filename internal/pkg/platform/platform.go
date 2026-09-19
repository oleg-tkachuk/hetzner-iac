// Package platform names the things more than one layer has to agree on.
//
// Not a grab-bag: the layers are separate Pulumi projects, so a value two of
// them must spell identically cannot be a literal in each. That is the same
// drift internal/pkg/clusterref prevents for stack outputs, and the same one
// internal/pkg/charts prevents for chart keys — a name in two places is a name
// that will eventually be in two versions.
package platform

import "github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"

// The storage classes the CSI driver in 10-node-platform registers, one per
// class of DATA rather than one per taste in reclaim policy.
//
// StorageClass is the default, and it reclaims `Delete`: a deleted claim takes
// the Hetzner volume with it. That is right for everything this platform runs
// today, because everything it runs is reconstructible from git — a cache, a
// build directory, a queue that can be drained.
//
// StorageClassDatabase reclaims `Retain`, and a claim has to ask for it by
// name. It exists for the one case where the volume holds the only copy of
// something: a database's data directory. Retain is not a backup and does not
// pretend to be — a database is backed up by the database, to object storage,
// with point-in-time recovery. What Retain buys is that the volume survives a
// deleted PersistentVolumeClaim, which is the accident that actually happens:
// an Argo CD prune of a directory somebody moved.
//
// Both were one literal in two halves once. Nothing would have reported the
// drift: a claim naming a class that does not exist is not rejected, it stays
// Pending, and the workload above it stays Pending with it — with no event on
// the Deployment saying why.
const (
	StorageClass         = "hcloud-volumes"
	StorageClassDatabase = "hcloud-volumes-db"
)

// DataNamespaceLabel marks a namespace whose volumes hold data that cannot be
// rebuilt, and it is what makes the two classes above a rule rather than a
// convention.
//
// `task cluster:smoke` refuses a claim in such a namespace that sits on the
// `Delete` class. Without that the taxonomy is a sentence in a document: a
// chart that omits storageClassName gets the default, which is exactly the
// class a database must not be on, and nothing says a word until the claim is
// deleted and the data is gone.
//
// A label rather than a list of namespaces, because the namespaces arrive with
// the workloads and the list would be edited in this repository every time
// somebody deploys one through Argo CD.
const DataNamespaceLabel = clusterspec.Name + "/holds-data"

// IngressClass is the class layers/40-ingress registers, and the one an
// Ingress elsewhere has to ask for by name.
//
// It was the literal "nginx" in the gitops layer, which stopped being true
// the moment Traefik replaced ingress-nginx: an Ingress naming a class no
// controller owns is accepted by the API server and then ignored, so the
// resource exists, looks right, and routes nothing.
const IngressClass = "traefik"

// IssuerName is the ClusterIssuer 30-cluster-services creates, and the one an
// Ingress elsewhere names in its cert-manager annotation.
//
// It was the literal "letsencrypt" in both layers, with a comment in the
// gitops one saying it had to match — a cross-layer contract written twice and
// compared by nothing. One rename in 30 away from the failure IngressClass
// above already had: an Ingress annotating a ClusterIssuer that does not exist
// is accepted, cert-manager ignores the annotation, and the Certificate sits
// pending with no event saying why.
const IssuerName = "letsencrypt"

// ACMESolverLabel is the label cert-manager puts on an HTTP-01 solver pod,
// and the one layers/20-network-policy selects to let Traefik reach it.
//
// A challenge solver is created when a certificate is issued and deleted when
// it is answered, so under the default deny the flow this names exists for
// about thirty seconds and only while an order is open. A misspelling here is
// therefore invisible for as long as Let's Encrypt keeps reusing a cached
// authorization — measured on this cluster: a reissue completed in twenty
// seconds with no solver pod at all — and then costs a renewal months later,
// as a Certificate stuck pending with its challenge timing out.
const ACMESolverLabel = "acme.cert-manager.io/http01-solver"

// Node ports the ingress load balancer forwards to.
//
// Two programs have to name the same two numbers and they never call each
// other: internal/pkg/values/traefik.yaml.tmpl asks Kubernetes to allocate exactly
// these for the ingress Service, and internal/pkg/hetzner points the load balancer's
// services and health checks at them.
//
// Pinned rather than allocated because a Pulumi-managed load balancer cannot
// forward to a number Kubernetes chooses after the fact. A mismatch is the
// quiet kind: the load balancer comes up, health-checks a closed port, reports
// every target unhealthy, and nothing else in the cluster looks wrong.
//
// Inside Kubernetes' default nodePort range, 30000-32767. Changing one is an
// apply that restarts ingress, so they are chosen to be memorable rather than
// to be changed.
const (
	IngressNodePortHTTP  = 30080
	IngressNodePortHTTPS = 30443
)

// The External Secrets Operator's one store, and what it needs to reach
// Pulumi ESC.
//
// Two layers have to agree on these: 30-cluster-services creates the store,
// and every ExternalSecret elsewhere names it. A store name that does not
// exist is the failure this package exists to prevent, in its quietest form —
// the ExternalSecret is accepted, never syncs, and the Secret it would have
// created is simply absent, so the pod that mounts it reports
// CreateContainerConfigError and says nothing about a secret store.
const (
	// SecretStore is the ClusterSecretStore's name.
	SecretStore = "pulumi-esc" // #nosec G101 -- the name of a store object, not a credential
	// SecretStoreNamespace is where its bootstrap credential lives, which is
	// the namespace the operator itself runs in.
	SecretStoreNamespace = "external-secrets"
	// SecretStoreTokenSecret and SecretStoreTokenKey are the Kubernetes Secret
	// the store authenticates with. Written by the layer from stack config,
	// and the one credential in this design that a person still holds.
	//
	// Both are NAMES. gosec reads a constant called …Token as a credential,
	// which is the right default and wrong here: the value it points at never
	// appears in this repository.
	SecretStoreTokenSecret = "pulumi-esc-token" // #nosec G101 -- the Secret's name, not its contents
	SecretStoreTokenKey    = "accessToken"      // #nosec G101 -- the key inside that Secret
)

// SecretStoreProject is the ESC project the environments live under.
//
// The repository's own name, so an organization that also uses ESC for
// something else keeps these apart, and so nothing new has to be configured:
// the environment is `<org>/<project>/<stack>` and the stack is already known.
const SecretStoreProject = clusterspec.Name

// SecretRefreshInterval is how often the operator re-reads a value.
//
// An hour rather than a minute: a secret changes when a person rotates it, and
// the cost of the shorter interval is a request per ExternalSecret per tick
// against an API with rate limits. ESO keeps the last value when a read fails,
// so a slow refresh degrades into a stale secret rather than an absent one.
const SecretRefreshInterval = "1h"

// The layers that install charts, spelled as their directories under layers/
// are. Held equal to those directories by TestWorkloadLayers_AreRealLayers.
//
// Here rather than in internal/pkg/workloads, where they were first written,
// because internal/pkg/charts now declares which layer installs each chart —
// and workloads imports charts, so the constants cannot live downstream of it.
//
// Named rather than written out at each use because the same string has to
// match a directory name and a layer's own idea of itself, and a comment
// saying which layer a chart belongs to matches nothing: `cilium` carried
// `// Layer 20 — CNI` for months while 10-node-platform installed it, and
// 20-network-policy installs no chart at all.
const (
	LayerNodePlatform    = "10-node-platform"
	LayerClusterServices = "30-cluster-services"
	LayerIngress         = "40-ingress"
	LayerGitOps          = "50-gitops"
)
