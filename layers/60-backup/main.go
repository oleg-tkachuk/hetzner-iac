// Command backup creates the destination the cluster's recovery point lives
// in.
//
// The only layer that creates nothing in Kubernetes. It reads the cluster tier
// for the token and the location, and everything it makes is Hetzner-side: a
// Storage Box, one subaccount confined to a directory, and the two passwords
// for them.
//
// # Why it is a layer at all
//
// Because it is applied and destroyed on the same terms as everything else,
// and because the layer machinery already resolves the cluster tier, holds the
// token as a secret and gives it a stack of its own. A backup destination in
// the cluster tier would be destroyed by `task cluster:destroy`, which is the
// one thing it must survive.
//
// # Why it is last
//
// Nothing depends on it, and it depends on nothing but the cluster's name and
// location. `task destroy` therefore removes it first, which is why the Storage
// Box carries delete protection: the remove is refused rather than obeyed.
package main

import (
	"fmt"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/hetzner"
	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/layer"

	"github.com/pulumi/pulumi-random/sdk/v4/go/random"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// PasswordLength is how long the generated passwords are.
//
// Hetzner's Storage Box password policy takes far less than this. Long because
// nothing types these: they are generated here, kept in Pulumi's encrypted
// state, and read back by whatever uploads a snapshot.
const PasswordLength = 48

// passwordArgs is how all three passwords are generated, because Hetzner
// refuses one that misses a character class.
//
// `Special: false` was the first shape, and the first live apply of this layer
// answered with 422 invalid_input: "The password must contain at least one
// upper case letter, one lower case letter, one number, and a special
// character". Length alone does not satisfy that — the four minimums do, and
// they are what this function exists to keep in one place for all three.
//
// OverrideSpecial narrows the set rather than taking the default. Every one of
// these values reaches a process through an environment variable —
// RESTIC_PASSWORD, RCLONE_CONFIG_BOX_PASS — where any byte is safe, but the
// operator also copies the restic key out of the stack by hand, and a
// password holding a quote, a backslash, a backtick or a dollar is one that
// breaks the moment it is pasted into a shell.
func passwordArgs() *random.RandomPasswordArgs {
	return &random.RandomPasswordArgs{
		Length:          pulumi.Int(PasswordLength),
		Special:         pulumi.Bool(true),
		MinUpper:        pulumi.Int(1),
		MinLower:        pulumi.Int(1),
		MinNumeric:      pulumi.Int(1),
		MinSpecial:      pulumi.Int(1),
		OverrideSpecial: pulumi.String(PasswordSpecialCharacters),
	}
}

// PasswordSpecialCharacters is the set OverrideSpecial allows: punctuation
// with no meaning to a shell. Deliberately excludes " ' ` \ $ ; | < > & and
// whitespace.
const PasswordSpecialCharacters = "!#%^*()-_=+[]{}:,.?" // #nosec G101 -- the alphabet a password may draw from, not a password

// Stack outputs. Named because a consumer — `task cluster:etcd:upload` today,
// an in-cluster job later — reads them by name, and a rename that only
// happened here would be a consumer reading nothing.
//
// TestBackupOutputs_AreTheNamesTheUploadTaskReads holds them equal to the
// names the task greps out of `pulumi stack output`, because that consumer is
// a shell script and cannot import a constant.
const (
	OutputHost               = "backupHost"
	OutputUsername           = "backupUsername"
	OutputPassword           = "backupPassword"
	OutputDirectory          = "backupDirectory"
	OutputRepositoryPassword = "backupRepositoryPassword"
)

// deploy creates the destination and publishes how to reach it.
func deploy(r *layer.Runner) error {
	// This layer's own provider: it creates Hetzner resources, not Kubernetes
	// ones, so it must not inherit r.Options — which carries the Kubernetes
	// provider.
	provider, err := hetzner.NewProvider(r.Ctx, r.Cluster.HcloudToken)
	if err != nil {
		return err
	}

	// Generated rather than configured, and that is the point of doing this in
	// Pulumi at all. The alternative — an operator minting a credential in a
	// console and pasting it into stack config — is the step that kept this
	// item in the backlog, and it puts the secret through a clipboard and a
	// shell history on the way.
	//
	// RandomPassword keeps its value in state, so a second apply does not
	// rotate the password and lock out whatever is using it.
	boxPassword, err := random.NewRandomPassword(r.Ctx, "box", passwordArgs())
	if err != nil {
		return fmt.Errorf("storage box password: %w", err)
	}

	snapshotPassword, err := random.NewRandomPassword(r.Ctx, "snapshots", passwordArgs())
	if err != nil {
		return fmt.Errorf("subaccount password: %w", err)
	}

	// The restic repository's encryption key, and the reason it is generated
	// here with the others: restic has no unencrypted mode, so this is a
	// credential that has to exist, and one an operator would otherwise invent
	// and keep somewhere.
	//
	// It is not interchangeable with the two above. Those open the box; this
	// one opens what is IN it, and losing it leaves the uploads on the box as
	// bytes nothing can read. See docs/operations.md — an operator should copy
	// it out of the stack once, because Pulumi's state then stops being the
	// only thing standing between the cluster and its backups.
	resticPassword, err := random.NewRandomPassword(r.Ctx, "restic", passwordArgs())
	if err != nil {
		return fmt.Errorf("restic repository password: %w", err)
	}

	box, err := hetzner.NewStorageBox(r.Ctx, "backup", hetzner.StorageBoxArgs{
		ClusterName:        r.Cluster.ClusterName,
		Location:           r.Cluster.Location,
		Type:               r.StringOr("storageBoxType", hetzner.DefaultStorageBoxType),
		Password:           boxPassword.Result,
		SubaccountPassword: snapshotPassword.Result,
		SnapshotsKept:      hetzner.DefaultSnapshotsKept,
		SnapshotHour:       hetzner.DefaultSnapshotHour,
		SnapshotMinute:     hetzner.DefaultSnapshotMinute,
	}, pulumi.Provider(provider))
	if err != nil {
		return err
	}

	r.Log.Step("storage-box", "sftp destination for etcd snapshots")

	r.Ctx.Export(OutputHost, box.Host)
	r.Ctx.Export(OutputUsername, box.Username)
	r.Ctx.Export(OutputDirectory, pulumi.String(box.Directory))
	// The subaccount's password, not the box's. The box's own password is in
	// state and deliberately not exported: nothing should be using it, and an
	// output is the thing somebody copies.
	r.Ctx.Export(OutputPassword, pulumi.ToSecret(snapshotPassword.Result))
	r.Ctx.Export(OutputRepositoryPassword, pulumi.ToSecret(resticPassword.Result))

	return nil
}

func main() {
	layer.Run(deploy)
}
