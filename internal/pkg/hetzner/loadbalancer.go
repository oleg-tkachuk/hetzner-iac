package hetzner

// Health check timings, for every load balancer this package creates: the API
// load balancer in front of the control plane, and the ingress load balancer
// in front of the node ports.
//
// Three failures at ten seconds takes a node out in about half a minute,
// which is slower than a pod restart and faster than a person notices.
//
// One block for both because the two are meant to behave the same way, and a
// comment in ingress.go used to say so while the API load balancer carried its
// own 10, 5 and 3. Nothing compared them.
const (
	healthCheckInterval = 10
	healthCheckTimeout  = 5
	healthCheckRetries  = 3
)
