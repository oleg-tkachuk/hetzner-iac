package hetzner

import (
	"fmt"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/pulumiopts"

	"github.com/pulumi/pulumi-hcloud/sdk/go/hcloud"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// DefaultStorageBoxType is the smallest Storage Box: 1 TB, 3.20 EUR a month
// gross in hel1, measured against the API.
//
// An etcd snapshot of this cluster is a few megabytes, so the smallest type is
// three orders of magnitude more than the thing it exists for. It is the
// cheapest destination Hetzner sells that is not the operator's own laptop,
// which is where these snapshots live today.
const DefaultStorageBoxType = "bx11"

// SnapshotHomeDirectory is the subaccount's home, and therefore the only path
// the cluster's credential can reach.
//
// A subaccount is confined to its home directory, which is why this exists at
// all rather than handing out the box's own password: a credential that can
// write the etcd snapshots cannot also read or delete anything else on the
// box.
const SnapshotHomeDirectory = "etcd-snapshots"

// Snapshot plan defaults: one a day, and ten kept.
//
// This is the BOX's own snapshot plan, not the etcd snapshots — a second layer
// underneath them. Uploading a corrupt snapshot over a good one is the failure
// it covers, and it is the failure a single directory of files cannot survive.
//
// 02:17 UTC rather than a round hour: everything else in this repository that
// runs on a schedule avoids the top of the hour, where every cron in the world
// contends.
const (
	DefaultSnapshotHour    = 2
	DefaultSnapshotMinute  = 17
	DefaultSnapshotsKept   = 10
	storageBoxUsernameHint = "the box's own account — the one this platform does NOT use"
)

// StorageBoxArgs is what a backup destination needs.
type StorageBoxArgs struct {
	// ClusterName scopes the name and the labels, so two clusters in one
	// project do not collide and `hcloud` can list one cluster's resources.
	ClusterName pulumi.StringInput
	// Location has to be a location Storage Boxes are sold in. The cluster's
	// own is the default and the right answer: the upload then stays inside
	// one region.
	Location pulumi.StringInput
	// Type is the Hetzner Storage Box type, from stack config.
	Type string
	// Password for the box's own account, and SubaccountPassword for the
	// confined one. Both are generated — see infra/backup — so neither is
	// ever typed, pasted or held anywhere but Pulumi's encrypted state.
	Password           pulumi.StringInput
	SubaccountPassword pulumi.StringInput
	// SnapshotsKept, SnapshotHour and SnapshotMinute drive the box's own
	// snapshot plan.
	SnapshotsKept  int
	SnapshotHour   int
	SnapshotMinute int
}

// StorageBox is the destination, reduced to what a consumer needs to write to
// it.
type StorageBox struct {
	// Host is the SFTP host, and Username the confined subaccount's login.
	// Together with the subaccount password they are the whole credential.
	Host     pulumi.StringOutput
	Username pulumi.StringOutput
	// Directory is the path inside the subaccount's home. Not a secret, and
	// exported so a restore knows where to look without reading the code.
	Directory string
}

// NewStorageBox creates a Hetzner Storage Box and one confined subaccount for
// writing snapshots to it.
//
// # Why a Storage Box rather than Object Storage
//
// The backup chain was recorded as blocked on the operator creating an Object
// Storage bucket and handing over S3 keys. It is not: a Storage Box is a
// resource this provider manages, so Pulumi creates the destination, generates
// both passwords, and schedules the box's own snapshots. Nothing is typed and
// no credential is pasted.
//
// The transport is SFTP rather than S3, which restic and rclone both speak.
// That is the trade, and it is a smaller one than a credential an operator
// has to mint by hand and then keep somewhere.
//
// # Where the API lives
//
// Storage Boxes are NOT on the Cloud API. `api.hetzner.cloud/v1/storage_boxes`
// answers `api route not found`; they are on the unified Hetzner API at
// `api.hetzner.com/v1/storage_boxes`, which the same project token reaches —
// both measured. The provider handles the difference; this
// matters only when reaching for curl to check something.
func NewStorageBox(
	ctx *pulumi.Context,
	name string,
	args StorageBoxArgs,
	opts ...pulumi.ResourceOption,
) (*StorageBox, error) {
	labels := pulumi.StringMap{
		clusterspec.LabelCluster:   args.ClusterName,
		clusterspec.LabelManagedBy: pulumi.String(clusterspec.ManagedBy),
	}

	box, err := hcloud.NewStorageBox(ctx, name, &hcloud.StorageBoxArgs{
		Name:           pulumi.Sprintf("%s-backup", args.ClusterName),
		StorageBoxType: pulumi.String(args.Type),
		Location:       args.Location,
		Password:       args.Password,
		Labels:         labels,
		// Everything off but SSH, which is what SFTP arrives over.
		//
		// Samba and WebDAV are network file protocols exposed to the internet
		// on a box holding the cluster's recovery point, and nothing here
		// speaks either. ZFS shows the snapshot directory to clients, which is
		// a way to read the box's own snapshots — and a way for a credential
		// confined to one directory to see outside it.
		AccessSettings: &hcloud.StorageBoxAccessSettingsArgs{
			SshEnabled:    pulumi.Bool(true),
			SambaEnabled:  pulumi.Bool(false),
			WebdavEnabled: pulumi.Bool(false),
			ZfsEnabled:    pulumi.Bool(false),
			// Reachable from outside Hetzner, because the thing that writes
			// these today is `task cluster:etcd:snapshot` on the operator's
			// own machine. Turn it off when an in-cluster job takes that over
			// — the nodes are inside the network.
			ReachableExternally: pulumi.Bool(true),
		},
		// The box's own snapshots, under the files this platform uploads. See
		// the constants: this is what an overwritten good snapshot is
		// recovered from.
		SnapshotPlan: &hcloud.StorageBoxSnapshotPlanArgs{
			MaxSnapshots: pulumi.Int(args.SnapshotsKept),
			Hour:         pulumi.Int(args.SnapshotHour),
			Minute:       pulumi.Int(args.SnapshotMinute),
		},
		// Guards the console, the API and the hcloud CLI: a delete through any
		// of those is refused until somebody clears this on purpose.
		//
		// It does NOT guard against this provider's own destroy, which is
		// worth stating because the opposite was written here and believed:
		// the provider disables the protection before deleting
		// (terraform-provider-hcloud, internal/storagebox/resource.go —
		// "Disable delete protection before deleting"), and a measured
		// `pulumi destroy` took the box with an uploaded snapshot on it in
		// 17 seconds. pulumi.Protect below is what covers that path.
		DeleteProtection: pulumi.Bool(true),
		// Protect is the other half, and it is Pulumi's rather than Hetzner's:
		// the engine refuses to delete or REPLACE this resource at all.
		//
		// Measured on a scratch stack: a destroy against a protected resource
		// fails during PREVIEW, so nothing in the stack is deleted — not even
		// the unprotected resources beside it. Removing it is a deliberate
		// second act, `pulumi state unprotect <urn>`, which `backup:destroy`
		// asks for by name rather than doing quietly.
		//
		// The cost is named because it is real: a change that forces
		// replacement — the box type, its location — fails too, with "unable
		// to replace resource ... as it is currently marked for protection",
		// and that one is fixed only by removing the option and applying. A
		// resize of the backup destination is therefore a code change plus an
		// apply, which is the correct price for a resource whose deletion
		// takes every etcd snapshot with it.
	}, pulumiopts.With(opts, pulumi.Protect(true))...)
	if err != nil {
		return nil, fmt.Errorf("hcloud storage box: %w", err)
	}

	subaccount, err := hcloud.NewStorageBoxSubaccount(ctx, name+"-snapshots",
		&hcloud.StorageBoxSubaccountArgs{
			StorageBoxId:  idToInt(box.ID()),
			HomeDirectory: pulumi.String(SnapshotHomeDirectory),
			Password:      args.SubaccountPassword,
			Description:   pulumi.String("etcd snapshots"),
			Labels:        labels,
			AccessSettings: &hcloud.StorageBoxSubaccountAccessSettingsArgs{
				SshEnabled:    pulumi.Bool(true),
				SambaEnabled:  pulumi.Bool(false),
				WebdavEnabled: pulumi.Bool(false),
				// Writable, because writing snapshots is its entire job. A
				// second, readonly subaccount is what a restore should use,
				// and belongs with the restore work rather than here.
				Readonly:            pulumi.Bool(false),
				ReachableExternally: pulumi.Bool(true),
			},
		}, opts...)
	if err != nil {
		return nil, fmt.Errorf("hcloud storage box subaccount: %w", err)
	}

	return &StorageBox{
		Host:      box.Server,
		Username:  subaccount.Username,
		Directory: SnapshotHomeDirectory,
	}, nil
}
