package hetzner

import (
	"fmt"
	"strings"

	"github.com/pulumi/pulumi-hcloud/sdk/go/hcloud"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// RecordTTL is how long a resolver may cache the ingress records.
//
// Five minutes, and short on purpose: the load balancer's address is not
// reserved — an LB IPv4 is neither a primary nor a floating IP — so a rebuilt
// environment serves from a new address, and the time the old one is cached is
// the time the domain is dark. Long TTLs buy resolver load this platform does
// not have.
const RecordTTL = 300

// Record types, spelled once. A typo reaches Hetzner as an unknown type
// rather than as a record for the wrong family, but only after the apply.
const (
	RecordA    = "A"
	RecordAAAA = "AAAA"
)

// ZoneApex is the record name Hetzner uses for the zone's own name.
//
// Hetzner's zone RRSets name records relative to the zone, and the zone itself
// is "@" rather than the empty string or the zone's own name. Getting this
// wrong creates a record for `example.com.example.com`, which resolves for
// nobody and looks correct in the console.
const ZoneApex = "@"

// RecordName returns the RRSet name for a domain inside a zone.
//
// Relative, because that is what Hetzner stores: `platform.example.com` in
// zone `example.com` is the record `platform`, and the zone's own name is the
// apex. The caller has already checked that the domain is inside the zone —
// the topology refuses the combination otherwise — so this only has to do the
// arithmetic.
func RecordName(domain, zone string) string {
	if domain == zone {
		return ZoneApex
	}

	return strings.TrimSuffix(domain, "."+zone)
}

// ZoneName is the zone's own spelling of its name, as the lookup returned it.
//
// The provider types LookupZoneResult.Name as a pointer, so it has to be
// checked: a bare dereference is a panic inside the Pulumi program, and a
// panic reaches the operator as a crashed provider rather than as a sentence
// about DNS. The lookup above has already failed on a zone that does not
// exist, which is what makes this the unlikely branch rather than the absent
// one.
//
// The lookup's answer rather than the requested name, even though they match
// today: the zone's stored spelling is what its records have to be filed
// under, and requested is here only so the failure says which zone it was
// asking about.
//
// Exported for the reason RecordName above is: it is the part of this file a
// test can reach without a Hetzner account.
func ZoneName(resolved *string, requested string) (string, error) {
	if resolved == nil || *resolved == "" {
		return "", fmt.Errorf(
			"hetzner dns zone %q: the lookup returned no name, so there is no zone to write "+
				"the records into", requested)
	}

	return *resolved, nil
}

// IngressRecordsArgs is what a pair of ingress records needs.
type IngressRecordsArgs struct {
	// Zone is the zone as delegated to Hetzner, and Domain the name inside it
	// the platform is served at.
	Zone   string
	Domain string
	// The load balancer's addresses, from NewIngressLoadBalancer.
	IPv4 pulumi.StringInput
	IPv6 pulumi.StringInput
}

// NewIngressRecords points a domain at the ingress load balancer.
//
// The zone is LOOKED UP, never created. A zone is delegated once, by pointing
// a registrar's NS records at Hetzner's nameservers, and that delegation
// outlives any cluster this repository builds. A zone created here would be a
// zone `pulumi destroy` deletes — taking every record in it, including the
// ones that have nothing to do with this platform.
//
// Both families, because the load balancer has both. An AAAA-less record on a
// dual-stack load balancer fails only for IPv6-only clients, which is the
// failure nobody testing from a laptop will see.
func NewIngressRecords(
	ctx *pulumi.Context,
	name string,
	args IngressRecordsArgs,
	opts ...pulumi.ResourceOption,
) error {
	zone, err := hcloud.LookupZone(ctx, &hcloud.LookupZoneArgs{
		Name: pulumi.StringRef(args.Zone),
	}, nil)
	if err != nil {
		return fmt.Errorf("hetzner dns zone %q: %w\n"+
			"the zone has to exist and be delegated to Hetzner: point the registrar's NS "+
			"records at Hetzner's nameservers, or leave metadata.dnsZone empty and manage "+
			"the records where the domain is hosted", args.Zone, err)
	}

	stored, err := ZoneName(zone.Name, args.Zone)
	if err != nil {
		return err
	}

	record := RecordName(args.Domain, args.Zone)

	for _, rrset := range []struct {
		suffix string
		kind   string
		value  pulumi.StringInput
	}{
		{suffix: "-a", kind: RecordA, value: args.IPv4},
		{suffix: "-aaaa", kind: RecordAAAA, value: args.IPv6},
	} {
		if _, err := hcloud.NewZoneRrset(ctx, name+rrset.suffix, &hcloud.ZoneRrsetArgs{
			Zone: pulumi.String(stored),
			Name: pulumi.String(record),
			Type: pulumi.String(rrset.kind),
			Ttl:  pulumi.Int(RecordTTL),
			Records: hcloud.ZoneRrsetRecordArray{
				hcloud.ZoneRrsetRecordArgs{Value: rrset.value},
			},
		}, opts...); err != nil {
			return fmt.Errorf("hetzner dns %s record for %s: %w", rrset.kind, args.Domain, err)
		}
	}

	return nil
}
