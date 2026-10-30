package clusterspec

import (
	"fmt"
	"net/netip"
)

// Addressing assigns every node a private address that is a pure function of
// the topology: which pool it belongs to and its ordinal within that pool.
//
// Static assignment rather than DHCP is deliberate. Talos machine
// configuration carries the node's address, so the address must be known at
// plan time — and it must be STABLE, because a node whose address changes is
// a node that gets replaced. Fixed per-pool slices are what make adding or
// resizing a pool leave every existing node's address untouched. A pool's
// slice is its position in workerPools unless addressSlot pins it, and only a
// pinned one survives removing or reordering the pools before it.
//
// Layout of the node subnet:
//
//	offset 0               network address (unusable)
//	offset 1               never assigned (see reservedPerSubnet)
//	offset 2..stride-3     control-plane nodes
//	offset stride-2        ingress load balancer
//	offset stride-1        API load balancer
//	offset stride..        worker slot 0
//	offset 2*stride..      worker slot 1, and so on
//
// The load balancers are pinned rather than left to Hetzner, which otherwise
// hands one the first free address in the subnet: an address a control-plane
// node is configured to take later, or races the first one for.
type Addressing struct {
	subnet netip.Prefix
	stride int
}

// reservedPerSubnet is the addresses at the start of the node subnet that no
// node is given: the network address, and the one after it. That one is the
// private network's gateway when the node subnet starts the range — the
// gateway itself is NetworkGateway's to derive — and it stays skipped
// otherwise, because moving the first node would readdress every node.
//
// Named because the count appeared three times — in the bounds check, in the
// message that check produces, and in the arithmetic that skips them.
const reservedPerSubnet = 2

// Load balancer addresses, counted down from the top of the control plane's
// slice so control-plane ordinals keep the offsets they always had.
const (
	apiLoadBalancerFromTop     = 1
	ingressLoadBalancerFromTop = 2

	// loadBalancerAddresses is how many addresses the slice's top holds back.
	loadBalancerAddresses = ingressLoadBalancerFromTop
)

// controlPlaneCapacity is how many control-plane nodes a slice of `stride`
// addresses seats between the reserved start and the load balancers.
func controlPlaneCapacity(stride int) int {
	return stride - reservedPerSubnet - loadBalancerAddresses
}

// NewAddressing prepares address allocation over a node subnet.
func NewAddressing(nodeSubnet string, stride int) (*Addressing, error) {
	if controlPlaneCapacity(stride) < 1 {
		// Slice 0 loses its reserved addresses and the load balancers', so a
		// smaller stride cannot seat even one control-plane node.
		return nil, fmt.Errorf("addressing stride %d is too small: a slice must hold the %d reserved addresses, the %d load balancers and a control-plane node",
			stride, reservedPerSubnet, loadBalancerAddresses)
	}

	prefix, err := netip.ParsePrefix(nodeSubnet)
	if err != nil {
		return nil, fmt.Errorf("node subnet %q: %w", nodeSubnet, err)
	}

	if !prefix.Addr().Is4() {
		// hcloud private networks are IPv4 only; an IPv6 prefix here would
		// produce addresses the API rejects much later.
		return nil, fmt.Errorf("node subnet %q must be IPv4", nodeSubnet)
	}

	return &Addressing{subnet: prefix.Masked(), stride: stride}, nil
}

// ControlPlaneIP returns the private address of control-plane node `ordinal`
// (0-based).
func (a *Addressing) ControlPlaneIP(ordinal int) (string, error) {
	if ordinal < 0 {
		return "", fmt.Errorf("control-plane ordinal %d is negative", ordinal)
	}

	if ordinal >= controlPlaneCapacity(a.stride) {
		return "", fmt.Errorf("control-plane ordinal %d does not fit in a %d-address slice: "+
			"%d addresses at its start are reserved and %d at its end are reserved for the load balancers",
			ordinal, a.stride, reservedPerSubnet, loadBalancerAddresses)
	}

	return a.at(reservedPerSubnet + ordinal)
}

// APILoadBalancerIP returns the API load balancer's private address. It is the
// cluster endpoint, so it must never move.
func (a *Addressing) APILoadBalancerIP() (string, error) {
	return a.at(a.stride - apiLoadBalancerFromTop)
}

// IngressLoadBalancerIP returns the ingress load balancer's private address.
func (a *Addressing) IngressLoadBalancerIP() (string, error) {
	return a.at(a.stride - ingressLoadBalancerFromTop)
}

// WorkerIP returns the private address of node `ordinal` in worker pool
// `poolIndex`, both 0-based.
func (a *Addressing) WorkerIP(poolIndex, ordinal int) (string, error) {
	if poolIndex < 0 {
		return "", fmt.Errorf("worker pool index %d is negative", poolIndex)
	}

	if ordinal < 0 {
		return "", fmt.Errorf("worker ordinal %d is negative", ordinal)
	}

	if ordinal >= a.stride {
		return "", fmt.Errorf("worker ordinal %d does not fit in a %d-address slice", ordinal, a.stride)
	}

	return a.at((poolIndex+1)*a.stride + ordinal)
}

// at converts an offset within the subnet to an address, refusing anything
// that would fall outside it.
func (a *Addressing) at(offset int) (string, error) {
	addr := a.subnet.Addr()

	octets := addr.As4()
	base := uint32(octets[0])<<24 | uint32(octets[1])<<16 | uint32(octets[2])<<8 | uint32(octets[3])

	// Compare in uint64 so a large offset cannot wrap around into a valid
	// address inside the subnet — which would silently collide with a node
	// that already holds it.
	size := uint64(1) << (addr.BitLen() - a.subnet.Bits())

	// #nosec G115 -- offset is non-negative (every caller checks it) and this
	// comparison IS the bounds check that makes the conversions below safe.
	if uint64(offset) >= size {
		return "", fmt.Errorf("offset %d is outside node subnet %s, which holds %d addresses", offset, a.subnet, size)
	}

	// #nosec G115 -- bounded by the size check above: 0 <= offset < size <= 2^32.
	result := base + uint32(offset)

	// #nosec G115 -- truncating each shifted octet is how an IPv4 address is
	// assembled; the narrowing is the operation, not an accident.
	return netip.AddrFrom4([4]byte{
		byte(result >> 24), byte(result >> 16), byte(result >> 8), byte(result),
	}).String(), nil
}
