package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optlist"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
)

// The provider's type tokens for the Hetzner resources a stack can hold, as
// pulumi-hcloud registers them. Held to the SDK by TestTypeTokens_AreTheSDKs.
const (
	typeCertificate        tokens.Type = "hcloud:index/certificate:Certificate"
	typeManagedCertificate tokens.Type = "hcloud:index/managedCertificate:ManagedCertificate"
	typeUploadedCert       tokens.Type = "hcloud:index/uploadedCertificate:UploadedCertificate"
	typeFirewall           tokens.Type = "hcloud:index/firewall:Firewall"
	typeFloatingIP         tokens.Type = "hcloud:index/floatingIp:FloatingIp"
	typeLoadBalancer       tokens.Type = "hcloud:index/loadBalancer:LoadBalancer"
	typeNetwork            tokens.Type = "hcloud:index/network:Network"
	typePlacementGroup     tokens.Type = "hcloud:index/placementGroup:PlacementGroup"
	typePrimaryIP          tokens.Type = "hcloud:index/primaryIp:PrimaryIp"
	typeServer             tokens.Type = "hcloud:index/server:Server"
	typeSnapshot           tokens.Type = "hcloud:index/snapshot:Snapshot"
	typeSSHKey             tokens.Type = "hcloud:index/sshKey:SshKey"
	typeStorageBox         tokens.Type = "hcloud:index/storageBox:StorageBox"
	typeSubaccount         tokens.Type = "hcloud:index/storageBoxSubaccount:StorageBoxSubaccount"
	typeVolume             tokens.Type = "hcloud:index/volume:Volume"
	typeZone               tokens.Type = "hcloud:index/zone:Zone"
	typeZoneRrset          tokens.Type = "hcloud:index/zoneRrset:ZoneRrset"
)

// zoneInput is the ZoneRrset input naming the zone its records are written to.
const zoneInput = "zone"

// heldKinds maps a stack's resource types onto the report's kinds. Types not
// here are attachments and records inside a resource — a subnet, a target, a
// service — which the API does not list on their own.
func heldKinds() map[tokens.Type]string {
	return map[tokens.Type]string{
		typeCertificate:        KindCertificate,
		typeManagedCertificate: KindCertificate,
		typeUploadedCert:       KindCertificate,
		typeFirewall:           KindFirewall,
		typeFloatingIP:         KindFloatingIP,
		typeLoadBalancer:       KindLoadBalancer,
		typeNetwork:            KindNetwork,
		typePlacementGroup:     KindPlacementGroup,
		typePrimaryIP:          KindPrimaryIP,
		typeServer:             KindServer,
		typeSnapshot:           KindSnapshot,
		typeSSHKey:             KindSSHKey,
		typeStorageBox:         KindStorageBox,
		typeSubaccount:         KindSubaccount,
		typeVolume:             KindVolume,
		typeZone:               KindZone,
	}
}

// stackStates is the part of a Pulumi workspace readHeld uses.
type stackStates interface {
	ListStacks(ctx context.Context, opts ...optlist.Option) ([]auto.StackSummary, error)
	ExportStack(ctx context.Context, stackName string) (apitype.UntypedDeployment, error)
}

// workspaceOpener opens the Pulumi workspace of one project directory.
type workspaceOpener func(ctx context.Context, dir string) (stackStates, error)

// openWorkspace is the workspaceOpener for the backend this machine is logged
// in to.
func openWorkspace(ctx context.Context, dir string) (stackStates, error) {
	return auto.NewLocalWorkspace(ctx, auto.WorkDir(dir))
}

// readHeld exports every stack of every project directory and collects what
// their states hold.
//
// Every stack, not only the one named: a Hetzner project shared by two
// environments holds both, and another environment's resource is its stack's
// to destroy, not an orphan. A directory with no stacks holds nothing — the
// truth after a teardown. A stack that exists and cannot be exported is an
// error, for the reason kubectlNames gives: an empty set would call every
// resource in the project an orphan.
func readHeld(ctx context.Context, dirs []string, open workspaceOpener) (Held, error) {
	held := NewHeld()

	for _, dir := range dirs {
		workspace, err := open(ctx, dir)
		if err != nil {
			return held, fmt.Errorf("%s: %w", dir, err)
		}

		read, err := addStacks(ctx, held, workspace, dir)
		if err != nil {
			return held, err
		}

		held.Stacks += read
	}

	return held, nil
}

// addStacks records every stack of one workspace in held, and says how many
// it read.
func addStacks(ctx context.Context, held Held, workspace stackStates, dir string) (int, error) {
	stacks, err := workspace.ListStacks(ctx)
	if err != nil {
		return 0, fmt.Errorf("%s: list stacks: %w", dir, err)
	}

	for _, stack := range stacks {
		exported, err := workspace.ExportStack(ctx, stack.Name)
		if err != nil {
			return 0, fmt.Errorf("%s: export %s: %w", dir, stack.Name, err)
		}

		if err := addDeployment(held, exported); err != nil {
			return 0, fmt.Errorf("%s: %s: %w", dir, stack.Name, err)
		}
	}

	return len(stacks), nil
}

// addDeployment records one exported state's Hetzner resources in held.
func addDeployment(held Held, exported apitype.UntypedDeployment) error {
	if err := checkSchema(exported.Version); err != nil {
		return err
	}

	var deployment apitype.DeploymentV3
	if err := json.Unmarshal(exported.Deployment, &deployment); err != nil {
		return fmt.Errorf("decode state: %w", err)
	}

	kinds := heldKinds()

	for _, res := range deployment.Resources {
		if err := addResource(held, kinds, res); err != nil {
			return err
		}
	}

	return nil
}

// checkSchema refuses a state schema this check does not read.
func checkSchema(version int) error {
	if version < apitype.DeploymentSchemaVersionCurrent || version > apitype.DeploymentSchemaVersionLatest {
		return fmt.Errorf("state schema version %d, this check reads %d to %d",
			version, apitype.DeploymentSchemaVersionCurrent, apitype.DeploymentSchemaVersionLatest)
	}

	return nil
}

// addResource records one state resource in held, when it is a listed kind.
func addResource(held Held, kinds map[tokens.Type]string, res apitype.ResourceV3) error {
	// A record set claims the zone it is written to. The zone itself is
	// usually made by hand and delegated, so no stack holds it.
	if res.Type == typeZoneRrset {
		addZone(held, res)

		return nil
	}

	kind, ok := kinds[res.Type]
	if !ok || res.ID == "" {
		return nil
	}

	// Every listed kind has a numeric ID in the API. One that does not parse
	// is a provider change, and matching around it would report the resource
	// as unheld.
	id, err := strconv.ParseInt(string(res.ID), 10, 64)
	if err != nil {
		return fmt.Errorf("%s %s: id %q is not the API's numeric id", res.Type, res.URN.Name(), res.ID)
	}

	held.Add(kind, id)

	return nil
}

// addZone records the zone a record set is written to.
func addZone(held Held, rrset apitype.ResourceV3) {
	if zone, ok := rrset.Inputs[zoneInput].(string); ok && zone != "" {
		held.Zones[zone] = true
	}
}
