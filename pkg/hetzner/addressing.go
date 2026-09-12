package hetzner

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
// resizing a pool leave every existing node's address untouched.
//
// Layout of the node subnet:
//
//	offset 0             network address (unusable)
//	offset 1             gateway (Hetzner reserves it)
//	offset 2..stride-1   control-plane nodes
//	offset stride..      worker pool 0
//	offset 2*stride..    worker pool 1, and so on
type Addressing struct {
	subnet netip.Prefix
	stride int
}

// Offsets inside a slice, which the layout above describes. Named because the
// count of reserved addresses appeared three times — in the bounds check, in
// the message that check produces, and in the arithmetic that skips them.
const (
	gatewayOffset = 1
	// reservedPerSubnet covers the network address and the gateway, the two
	// Hetzner does not let a server hold.
	reservedPerSubnet = 2
)

// NewAddressing prepares address allocation over a node subnet.
func NewAddressing(nodeSubnet string, stride int) (*Addressing, error) {
	if stride < reservedPerSubnet {
		// Slice 0 loses two addresses to the network and the gateway, so a
		// stride below 2 cannot seat even one control-plane node.
		return nil, fmt.Errorf("addressing stride %d is too small: a slice must hold at least the network and gateway addresses", stride)
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

	if ordinal+reservedPerSubnet >= a.stride {
		return "", fmt.Errorf("control-plane ordinal %d does not fit in a %d-address slice (%d addresses are reserved)",
			ordinal, a.stride, reservedPerSubnet)
	}

	return a.at(reservedPerSubnet + ordinal)
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

// Gateway is the subnet's gateway, which Hetzner fixes at the first usable
// address. Talos needs it to build the node's default route.
func (a *Addressing) Gateway() (string, error) {
	return a.at(gatewayOffset)
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
