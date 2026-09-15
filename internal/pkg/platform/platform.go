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

// IngressClass is the class layers/40-ingress registers, and the one an
// Ingress elsewhere has to ask for by name.
//
// It was the literal "nginx" in the gitops layer, which stopped being true
// the moment Traefik replaced ingress-nginx: an Ingress naming a class no
// controller owns is accepted by the API server and then ignored, so the
// resource exists, looks right, and routes nothing.
const IngressClass = "traefik"

// ProbeLocation is the Hetzner location every test fixture and every render
// probe uses, and it is one spelling on purpose.
//
// It was sixteen: three fixtures in internal/pkg/hetzner, one each in
// internal/pkg/layer, internal/pkg/clusterref and layers/10-node-platform,
// three in tools/image, two in tools/topology, plus the CSI render probe in
// internal/pkg/chartsettings and the template probe in internal/pkg/values.
// Every one of them wrote `hel1`.
//
// A fixture inventing its own is not harmless. The topology validator rejects
// a location it does not know, and the CSI render probe is passed through to
// an env var and asserted on the rendered output — so a fixture naming a
// location the validator or the schema has never heard of tests something
// that cannot happen, and reports green.
//
// Three things hold it, all in internal/ci because they cross packages: it is
// a member of hetzner.Locations, it is what the schema's enum accepts, and it
// is what infra/cluster/cluster.example.yaml actually names. That last one is
// the anchor the operator can see — the committed cluster config, which is
// where a location is supposed to come from.
//
// Nothing in production reads this. The real location travels
// placement.location → hetzner.Topology → clusterref.OutputLocation →
// r.Cluster.Location, and the layers that need it (the ingress load balancer,
// the Storage Box, the CSI controller) take it from there.
const ProbeLocation = "hel1"

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
