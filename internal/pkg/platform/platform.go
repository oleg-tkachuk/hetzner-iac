// Package platform names the things more than one layer has to agree on.
//
// Not a grab-bag: the layers are separate Pulumi projects, so a value two of
// them must spell identically cannot be a literal in each. That is the same
// drift internal/pkg/clusterref prevents for stack outputs, and the same one
// internal/pkg/chartsettings prevents for chart keys — a name in two places is a name
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
