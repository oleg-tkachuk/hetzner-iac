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
// # Why it is a tier rather than a layer
//
// Nothing depends on it, and it depends on nothing but the cluster's name and
// location. As a layer it was destroyed FIRST by `layer=all`, ahead of layers
// it did not depend on.
//
// The Storage Box's delete protection does not save it from that, which was
// measured rather than assumed: `task backup:destroy` took the box, its
// subaccount and an uploaded snapshot in 17 seconds with `deleteProtection:
// true` in state and read back as true. The provider disables the protection
// itself before deleting — hetznercloud/terraform-provider-hcloud,
// internal/storagebox/resource.go, literally "Disable delete protection before
// deleting". What does stop a destroy is `pulumi.Protect(true)`, which the box
// carries, and being a tier: a protected resource fails the destroy in preview,
// and no walk reaches this project in the first place.
//
// # Why it has no component set
//
// The only layer that builds its resources by hand, and the reason is the
// passwords. A layer.Components table hands a component its dependencies as
// []pulumi.Resource, which is all a chart or a manifest needs; the Storage Box
// needs the password VALUES, and it takes them as arguments. That is a
// stronger dependency than After — the engine derives it from the data — and
// it is not expressible through the table. layertest.Check would also assert
// nothing here, since it pairs charts with their workloads and this layer
// installs none. internal/ci/layers_test.go names this layer as the exception,
// so a second one cannot appear by omission.
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
// that both Hetzner accepts and a shell leaves alone.
//
// Two constraints, and each one has cost a failed apply. It excludes
// " \' ` \\ $ ; | < > & and whitespace, because the restic key is pasted by
// hand. It also excludes [ and ], because Hetzner rejects them — see
// HetznerPasswordSpecialCharacters below, which this must remain a subset of.
const PasswordSpecialCharacters = "!#%^*()-_=+{}:,.?" // #nosec G101 -- the alphabet a password may draw from, not a password

// HetznerPasswordSpecialCharacters is what the Storage Box API accepts,
// quoted from its own 422:
//
//	The password can only contain these characters: a-z A-Z Ä Ö Ü ä ö ü ß
//	0-9 ^ ° ! § $ % / ( ) = ? + # - . , ; : ~ * @ { } _ &
//
// The ASCII punctuation of that list. The letters and digits are not here
// because RandomPassword draws those anyway, and the non-ASCII ones — Ä ö ß °
// § — are deliberately left out of anything this generates: they survive an
// environment variable, and they are a liability in a value an operator
// retypes.
//
// It is written down because the alphabet above has to be a subset of it and
// nothing else says so. The failure when it is not is a 422 at APPLY time,
// after the passwords have been generated and some resources created — which
// is exactly what happened: `[` and `]` were in the set, and the Storage Box
// and its subaccount errored while five other resources had been created.
const HetznerPasswordSpecialCharacters = "^!$%/()=?+#-.,;:~*@{}_&" // #nosec G101 -- the alphabet the API permits, not a password

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
	// Its own provider, and the only one this project has: the runner comes
	// from layer.NewWithoutKubernetes, so there is no Kubernetes provider to
	// inherit or to avoid inheriting. That used to be a comment warning about
	// r.Options; it is now a fact about the runner.
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
	// bytes nothing can read. See docs/recovery.md — an operator should copy
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
	layer.RunWithoutKubernetes(deploy)
}
