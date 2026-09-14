package hetzner_test

import (
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/hetzner"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRecordName is the arithmetic every record depends on.
//
// Hetzner stores an RRSet name RELATIVE to its zone, and the zone's own name
// is the apex marker rather than the empty string or the zone repeated. Both
// wrong answers are accepted by the API: one creates a record for
// `example.com.example.com` and the other for a label called `example.com`,
// and each resolves for nobody while looking right in the console.
func TestRecordName(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		domain, zone, want string
	}{
		"the zone itself is the apex": {
			domain: "example.com", zone: "example.com", want: hetzner.ZoneApex,
		},
		"one label inside the zone": {
			domain: "platform.example.com", zone: "example.com", want: "platform",
		},
		"two labels inside the zone": {
			domain: "argocd.dev.example.com", zone: "example.com", want: "argocd.dev",
		},
		"a zone that is itself a subdomain": {
			domain: "argocd.dev.example.com", zone: "dev.example.com", want: "argocd",
		},
		// A registrable name whose zone cut is not the last two labels. This
		// is the case that makes dnsZone a field rather than something
		// derived: nothing here has to know about the public suffix list.
		"a multi-label public suffix": {
			domain: "platform.example.co.uk", zone: "example.co.uk", want: "platform",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, test.want, hetzner.RecordName(test.domain, test.zone))
		})
	}
}

// runRecords creates the ingress records under the mock monitor.
func runRecords(t *testing.T, zone, domain string) *recorder {
	t.Helper()

	rec := newRecorder()

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		return hetzner.NewIngressRecords(ctx, "ingress", hetzner.IngressRecordsArgs{
			Zone:   zone,
			Domain: domain,
			IPv4:   pulumi.String("203.0.113.10"),
			IPv6:   pulumi.String("2001:db8::10"),
		})
	}, pulumi.WithMocks("hetzner-iac", "test", rec))

	require.NoError(t, err)

	return rec
}

// TestNewIngressRecords_BothFamilies is the half an IPv4-only record misses.
//
// A Hetzner load balancer has an IPv4 and an IPv6. An A record alone fails
// only for IPv6-only clients, which is the failure nobody testing from a
// laptop will see, and it looks like the site being down rather than like a
// missing record.
func TestNewIngressRecords_BothFamilies(t *testing.T) {
	t.Parallel()

	registered := runRecords(t, "example.com", "platform.example.com").
		of("hcloud:index/zoneRrset:ZoneRrset")
	require.Len(t, registered, 2, "one A and one AAAA")

	byType := map[string]resource.PropertyMap{}
	for _, rrset := range registered {
		byType[rrset["type"].StringValue()] = rrset
	}

	a, found := byType[hetzner.RecordA]
	require.True(t, found, "no A record")
	aaaa, found := byType[hetzner.RecordAAAA]
	require.True(t, found, "no AAAA record")

	for kind, rrset := range map[string]resource.PropertyMap{
		hetzner.RecordA: a, hetzner.RecordAAAA: aaaa,
	} {
		// Anchored to the zone, named relative to it.
		assert.Equal(t, "example.com", rrset["zone"].StringValue(), "%s is in the wrong zone", kind)
		assert.Equal(t, "platform", rrset["name"].StringValue(), "%s has the wrong name", kind)
		assert.Equal(t, float64(hetzner.RecordTTL), rrset["ttl"].NumberValue())
	}

	// The addresses land in the right family. Swapping them is accepted by
	// the API and resolves to nothing.
	assert.Equal(t, "203.0.113.10",
		a["records"].ArrayValue()[0].ObjectValue()["value"].StringValue())
	assert.Equal(t, "2001:db8::10",
		aaaa["records"].ArrayValue()[0].ObjectValue()["value"].StringValue())
}

// TestNewIngressRecords_ApexDomain covers the environment served at the zone's
// own name rather than at a label inside it.
func TestNewIngressRecords_ApexDomain(t *testing.T) {
	t.Parallel()

	registered := runRecords(t, "example.com", "example.com").
		of("hcloud:index/zoneRrset:ZoneRrset")
	require.Len(t, registered, 2)

	for _, rrset := range registered {
		assert.Equal(t, hetzner.ZoneApex, rrset["name"].StringValue(),
			"the zone's own name must be the apex marker, not the zone repeated")
	}
}

// TestNewIngressRecords_CreatesNoZone is the property that keeps a
// `pulumi destroy` from taking a delegation with it.
//
// A zone is created once, by pointing a registrar's NS records at Hetzner, and
// it holds records that have nothing to do with this platform. Creating it
// here would make it this stack's to delete.
func TestNewIngressRecords_CreatesNoZone(t *testing.T) {
	t.Parallel()

	assert.Empty(t, runRecords(t, "example.com", "platform.example.com").
		of("hcloud:index/zone:Zone"),
		"the zone is looked up, never created: a zone this stack owns is a zone "+
			"`pulumi destroy` deletes, with every record in it")
}
