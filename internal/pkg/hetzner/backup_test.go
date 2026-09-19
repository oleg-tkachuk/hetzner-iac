package hetzner_test

import (
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterref"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/hetzner"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runStorageBox creates a backup destination under the mock monitor and
// returns what was registered.
func runStorageBox(t *testing.T) *recorder {
	t.Helper()

	rec := newRecorder()

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := hetzner.NewStorageBox(ctx, "backup", hetzner.StorageBoxArgs{
			ClusterName:        pulumi.String(testCluster),
			Location:           pulumi.String(clusterref.ProbeLocation),
			Type:               hetzner.DefaultStorageBoxType,
			Password:           pulumi.String("generated-box-password"),
			SubaccountPassword: pulumi.String("generated-subaccount-password"),
			SnapshotsKept:      hetzner.DefaultSnapshotsKept,
			SnapshotHour:       hetzner.DefaultSnapshotHour,
			SnapshotMinute:     hetzner.DefaultSnapshotMinute,
		})

		return err
	}, pulumi.WithMocks("hetzner-iac", "test", rec))

	require.NoError(t, err)

	return rec
}

func TestStorageBox_IsPlacedAndLabelledWithTheCluster(t *testing.T) {
	t.Parallel()

	registered := runStorageBox(t).of("hcloud:index/storageBox:StorageBox")
	require.Len(t, registered, 1)

	got := registered[0]

	assert.Equal(t, testCluster+"-backup", got["name"].StringValue())
	assert.Equal(t, hetzner.DefaultStorageBoxType, got["storageBoxType"].StringValue())

	// The cluster's own location: the upload then stays inside one region.
	assert.Equal(t, clusterref.ProbeLocation, got["location"].StringValue())

	labels := got["labels"].ObjectValue()
	assert.Equal(t, testCluster,
		labels[resource.PropertyKey(clusterspec.LabelCluster)].StringValue())
}

// TestStorageBox_ExposesOnlySSH is the security shape, and every one of these
// is a protocol on a box holding the cluster's recovery point.
func TestStorageBox_ExposesOnlySSH(t *testing.T) {
	t.Parallel()

	registered := runStorageBox(t).of("hcloud:index/storageBox:StorageBox")
	require.Len(t, registered, 1)

	access := registered[0]["accessSettings"].ObjectValue()

	// SFTP arrives over SSH, so this one is the reason the box exists.
	assert.True(t, access["sshEnabled"].BoolValue())

	for _, off := range []resource.PropertyKey{"sambaEnabled", "webdavEnabled", "zfsEnabled"} {
		assert.False(t, access[off].BoolValue(),
			"%s is enabled on a box holding the recovery point, and nothing here speaks it", off)
	}

	// ZFS specifically: it shows the box's own snapshot directory to clients,
	// which is a way for a credential confined to one directory to see
	// outside it.
	assert.False(t, access["zfsEnabled"].BoolValue())
}

// TestStorageBox_CarriesDeleteProtection pins the flag, not a promise about
// what it stops.
//
// It stops a delete through the console, the API or the hcloud CLI. It does
// not stop `pulumi destroy`: the provider clears the protection first, which
// was measured — see the comment beside DeleteProtection. The test is still
// worth having, because the flag being dropped would remove the one guard
// against a delete by hand.
func TestStorageBox_CarriesDeleteProtection(t *testing.T) {
	t.Parallel()

	registered := runStorageBox(t).of("hcloud:index/storageBox:StorageBox")
	require.Len(t, registered, 1)

	assert.True(t, registered[0]["deleteProtection"].BoolValue(),
		"a destination anybody can delete by hand from the console is not a destination")
}

// TestStorageBox_KeepsItsOwnSnapshotsUnderTheUploads covers the failure a
// directory of files cannot survive: a corrupt snapshot written over a good
// one.
func TestStorageBox_KeepsItsOwnSnapshotsUnderTheUploads(t *testing.T) {
	t.Parallel()

	registered := runStorageBox(t).of("hcloud:index/storageBox:StorageBox")
	require.Len(t, registered, 1)

	plan := registered[0]["snapshotPlan"].ObjectValue()

	assert.Equal(t, float64(hetzner.DefaultSnapshotsKept), plan["maxSnapshots"].NumberValue())
	assert.Equal(t, float64(hetzner.DefaultSnapshotHour), plan["hour"].NumberValue())
	assert.Equal(t, float64(hetzner.DefaultSnapshotMinute), plan["minute"].NumberValue())
}

// TestStorageBox_ConfinesTheCredentialThatWritesSnapshots is why a subaccount
// exists instead of handing out the box's own password.
func TestStorageBox_ConfinesTheCredentialThatWritesSnapshots(t *testing.T) {
	t.Parallel()

	registered := runStorageBox(t).of("hcloud:index/storageBoxSubaccount:StorageBoxSubaccount")
	require.Len(t, registered, 1)

	got := registered[0]

	// A subaccount cannot leave its home directory, so this is the whole
	// confinement: the credential that writes snapshots can reach nothing
	// else on the box.
	assert.Equal(t, hetzner.SnapshotHomeDirectory, got["homeDirectory"].StringValue())

	// Its own password, not the box's — and the provider marks it SECRET,
	// which is why this reads through the wrapper rather than asserting a
	// string. Worth pinning in both directions: a provider that stopped
	// marking it would put the credential in plaintext in the state file and
	// in every preview diff.
	password := got["password"]
	require.True(t, password.IsSecret(), "the subaccount password is not marked secret")

	assert.Equal(t, "generated-subaccount-password", password.SecretValue().Element.StringValue())
	assert.NotEqual(t, "generated-box-password", password.SecretValue().Element.StringValue())

	access := got["accessSettings"].ObjectValue()
	assert.True(t, access["sshEnabled"].BoolValue())
	assert.False(t, access["sambaEnabled"].BoolValue())
	assert.False(t, access["webdavEnabled"].BoolValue())

	// Writable, because writing snapshots is its entire job. A readonly
	// subaccount is what a restore should use.
	assert.False(t, access["readonly"].BoolValue())
}

// TestStorageBox_ReturnsTheWholeCredentialsLocation keeps the return value
// usable: a consumer needs the host, the login and the path, and the password
// it already holds.
func TestStorageBox_ReturnsTheWholeCredentialsLocation(t *testing.T) {
	t.Parallel()

	var box *hetzner.StorageBox

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		got, err := hetzner.NewStorageBox(ctx, "backup", hetzner.StorageBoxArgs{
			ClusterName:        pulumi.String(testCluster),
			Location:           pulumi.String(clusterref.ProbeLocation),
			Type:               hetzner.DefaultStorageBoxType,
			Password:           pulumi.String("p"),
			SubaccountPassword: pulumi.String("q"),
			SnapshotsKept:      hetzner.DefaultSnapshotsKept,
			SnapshotHour:       hetzner.DefaultSnapshotHour,
			SnapshotMinute:     hetzner.DefaultSnapshotMinute,
		})
		box = got

		return err
	}, pulumi.WithMocks("hetzner-iac", "test", newRecorder()))

	require.NoError(t, err)
	require.NotNil(t, box)

	assert.Equal(t, hetzner.SnapshotHomeDirectory, box.Directory)
}

// TestStorageBox_IsProtectedFromPulumisOwnDestroy is the assertion Hetzner's
// DeleteProtection cannot carry.
//
// The two are different mechanisms and only one of them stops this repository
// from deleting its own backups. DeleteProtection is an INPUT, and the provider
// clears it before deleting — measured: a destroy took the box, its subaccount
// and an uploaded snapshot in 17 seconds with the flag set. pulumi.Protect is a
// resource OPTION held in state, and the engine refuses the delete during
// preview, so nothing in the stack is touched.
//
// Read off the register RPC rather than the inputs, because an option is not an
// input — the same way deleteBeforeReplace is checked in cluster_test.go.
func TestStorageBox_IsProtectedFromPulumisOwnDestroy(t *testing.T) {
	t.Parallel()

	rec := runStorageBox(t)

	assert.True(t, rec.isProtected("backup"),
		"the Storage Box is not registered with pulumi.Protect, so `pulumi destroy` deletes it "+
			"and every etcd snapshot on it; Hetzner's DeleteProtection does not stop that")
}
