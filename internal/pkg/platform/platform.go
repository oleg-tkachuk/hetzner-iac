// Package platform names the things more than one layer has to agree on.
//
// Not a grab-bag: the layers are separate Pulumi projects, so a value two of
// them must spell identically cannot be a literal in each. That is the same
// drift internal/pkg/clusterref prevents for stack outputs, and the same one
// internal/pkg/chartsettings prevents for chart keys — a name in two places is a name
// that will eventually be in two versions.
package platform

// StorageClass is the class the CSI driver in 10-node-platform registers, and
// the one every PersistentVolumeClaim elsewhere asks for.
//
// It was a literal in both halves. Nothing would have reported a drift: a
// claim naming a class that does not exist is not rejected, it simply stays
// Pending, and the workload above it stays Pending with it — with no event on
// the Deployment saying why.
const StorageClass = "hcloud-volumes"

// IngressClass is the class layers/30-cluster-services registers, and the one an
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
