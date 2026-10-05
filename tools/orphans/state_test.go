package main

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/pulumi/pulumi-hcloud/sdk/go/hcloud"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deployment is an exported state holding resources of the given types and
// IDs, as `pulumi stack export` writes it.
func deployment(t *testing.T, version int, resources ...apitype.ResourceV3) apitype.UntypedDeployment {
	t.Helper()

	raw, err := json.Marshal(apitype.DeploymentV3{Resources: resources})
	require.NoError(t, err)

	return apitype.UntypedDeployment{Version: version, Deployment: raw}
}

func res(typ tokens.Type, id string) apitype.ResourceV3 {
	return apitype.ResourceV3{
		URN:  resource.NewURN("dev", "hetzner-cluster", "", typ, "platform-dev"),
		Type: typ, ID: resource.ID(id),
	}
}

// TestAddDeployment_ReadsWhatDevHolds is the cluster tier's state on dev:
// every top-level ID is the API's, and the attachments inside a resource are
// not listed on their own.
func TestAddDeployment_ReadsWhatDevHolds(t *testing.T) {
	t.Parallel()

	rrset := res(typeZoneRrset, "example.com/ingress/A")
	rrset.Inputs = map[string]any{zoneInput: "example.com"}

	held := NewHeld()
	require.NoError(t, addDeployment(held, deployment(t, apitype.DeploymentSchemaVersionCurrent,
		res("pulumi:pulumi:Stack", ""),
		res("pulumi:providers:hcloud", "4b1c0a1e-provider"),
		res(typeNetwork, "12655834"),
		res("hcloud:index/networkSubnet:NetworkSubnet", "12655834-10.0.1.0/24"),
		res(typeFirewall, "11627399"),
		res(typeLoadBalancer, "7848680"),
		res("hcloud:index/loadBalancerService:LoadBalancerService", "7848680__6443"),
		res(typePrimaryIP, "149807094"),
		res(typeServer, "166049291"),
		res(typeStorageBox, "654823"),
		res(typeSubaccount, "313492"),
		res(typeManagedCertificate, "77"),
		rrset,
	)))

	for kind, id := range map[string]int64{
		KindNetwork: 12655834, KindFirewall: 11627399, KindLoadBalancer: 7848680,
		KindPrimaryIP: 149807094, KindServer: 166049291, KindStorageBox: 654823,
		KindSubaccount: 313492, KindCertificate: 77,
	} {
		assert.True(t, held.Holds(kind, id), kind)
	}

	assert.True(t, held.Zones["example.com"], "a record set claims its zone")
	assert.Len(t, held.IDs, 8, "subnets, services and providers are not listed kinds")
}

func TestAddDeployment_Refuses(t *testing.T) {
	t.Parallel()

	for name, exported := range map[string]apitype.UntypedDeployment{
		"a non-numeric id": deployment(t, apitype.DeploymentSchemaVersionCurrent, res(typeServer, "server-0")),
		"an older schema":  deployment(t, apitype.DeploymentSchemaVersionCurrent-1),
		"a newer schema":   deployment(t, apitype.DeploymentSchemaVersionLatest+1),
		"undecodable":      {Version: apitype.DeploymentSchemaVersionCurrent, Deployment: json.RawMessage(`[]`)},
	} {
		assert.Error(t, addDeployment(NewHeld(), exported), name)
	}

	held := NewHeld()
	require.NoError(t, addDeployment(held, deployment(t, apitype.DeploymentSchemaVersionLatest, res(typeServer, ""))),
		"a resource not yet created has no ID, and holds nothing")
	assert.Empty(t, held.IDs)
}

// errOf keeps a constructor's error and drops the resource.
func errOf[T any](_ T, err error) error {
	return err
}

// tokenRecorder records the type token of every resource registered.
type tokenRecorder struct {
	mu     sync.Mutex
	tokens map[tokens.Type]bool
}

func (r *tokenRecorder) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.tokens[tokens.Type(args.TypeToken)] = true

	return args.Name, args.Inputs, nil
}

func (r *tokenRecorder) Call(pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

// TestTypeTokens_AreTheSDKs holds every token this check matches on to the
// one pulumi-hcloud registers. A misspelt token claims nothing, and every
// resource of that kind would be reported.
func TestTypeTokens_AreTheSDKs(t *testing.T) {
	t.Parallel()

	recorder := &tokenRecorder{tokens: map[tokens.Type]bool{}}

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		str := func(value string) pulumi.StringInput { return pulumi.String(value) }

		for _, register := range []func() error{
			func() error {
				return errOf(hcloud.NewCertificate(ctx, "c", &hcloud.CertificateArgs{Certificate: str("x"), PrivateKey: str("x")}))
			},
			func() error {
				return errOf(hcloud.NewManagedCertificate(ctx, "mc", &hcloud.ManagedCertificateArgs{
					DomainNames: pulumi.StringArray{str("example.com")},
				}))
			},
			func() error {
				return errOf(hcloud.NewUploadedCertificate(ctx, "uc", &hcloud.UploadedCertificateArgs{
					Certificate: str("x"), PrivateKey: str("x"),
				}))
			},
			func() error {
				return errOf(hcloud.NewFirewall(ctx, "fw", &hcloud.FirewallArgs{}))
			},
			func() error {
				return errOf(hcloud.NewFloatingIp(ctx, "fip", &hcloud.FloatingIpArgs{Type: str("ipv4")}))
			},
			func() error {
				return errOf(hcloud.NewLoadBalancer(ctx, "lb", &hcloud.LoadBalancerArgs{LoadBalancerType: str("lb11")}))
			},
			func() error {
				return errOf(hcloud.NewNetwork(ctx, "net", &hcloud.NetworkArgs{IpRange: str("10.0.0.0/16")}))
			},
			func() error {
				return errOf(hcloud.NewPlacementGroup(ctx, "pg", &hcloud.PlacementGroupArgs{Type: str("spread")}))
			},
			func() error {
				return errOf(hcloud.NewPrimaryIp(ctx, "pip", &hcloud.PrimaryIpArgs{
					AssigneeType: str("server"), AutoDelete: pulumi.Bool(false), Type: str("ipv4"),
				}))
			},
			func() error {
				return errOf(hcloud.NewServer(ctx, "srv", &hcloud.ServerArgs{ServerType: str("cx23")}))
			},
			func() error {
				return errOf(hcloud.NewSnapshot(ctx, "snap", &hcloud.SnapshotArgs{ServerId: pulumi.Int(1)}))
			},
			func() error {
				return errOf(hcloud.NewSshKey(ctx, "key", &hcloud.SshKeyArgs{PublicKey: str("ssh-ed25519 x")}))
			},
			func() error {
				return errOf(hcloud.NewStorageBox(ctx, "box", &hcloud.StorageBoxArgs{
					Location: str("fsn1"), Password: str("x"), StorageBoxType: str("bx11"),
				}))
			},
			func() error {
				return errOf(hcloud.NewStorageBoxSubaccount(ctx, "sub", &hcloud.StorageBoxSubaccountArgs{
					HomeDirectory: str("x"), Password: str("x"), StorageBoxId: pulumi.Int(1),
				}))
			},
			func() error {
				return errOf(hcloud.NewVolume(ctx, "vol", &hcloud.VolumeArgs{Size: pulumi.Int(10)}))
			},
			func() error {
				return errOf(hcloud.NewZone(ctx, "zone", &hcloud.ZoneArgs{Mode: str("primary")}))
			},
			func() error {
				return errOf(hcloud.NewZoneRrset(ctx, "rrset", &hcloud.ZoneRrsetArgs{
					Name: str("ingress"), Type: str("A"), Zone: str("example.com"),
					Records: hcloud.ZoneRrsetRecordArray{hcloud.ZoneRrsetRecordArgs{Value: str("192.0.2.1")}},
				}))
			},
		} {
			if err := register(); err != nil {
				return err
			}
		}

		return nil
	}, pulumi.WithMocks("hetzner-iac", "test", recorder))
	require.NoError(t, err)

	for token := range heldKinds() {
		assert.True(t, recorder.tokens[token], "%s is not a type pulumi-hcloud registers", token)
	}

	assert.True(t, recorder.tokens[typeZoneRrset], "%s is not a type pulumi-hcloud registers", typeZoneRrset)
}
