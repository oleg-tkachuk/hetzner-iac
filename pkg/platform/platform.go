// Package platform names the things more than one layer has to agree on.
//
// Not a grab-bag: the layers are separate Pulumi projects, so a value two of
// them must spell identically cannot be a literal in each. That is the same
// drift pkg/clusterref prevents for stack outputs, and the same one
// pkg/chartsettings prevents for chart keys — a name in two places is a name
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

// IngressClass is the class layers/40-ingress registers, and the one an
// Ingress elsewhere has to ask for by name.
//
// It was the literal "nginx" in the gitops layer, which stopped being true
// the moment Traefik replaced ingress-nginx: an Ingress naming a class no
// controller owns is accepted by the API server and then ignored, so the
// resource exists, looks right, and routes nothing.
const IngressClass = "traefik"
