// Command object-storage creates the buckets in Hetzner Object Storage that
// outlive the cluster.
//
// It is the one layer with no Kubernetes in it, and the one that must be
// applicable before a cluster exists and survive its destruction. Both follow
// from what the buckets hold: Pulumi state, which a cluster's own state cannot
// contain, and Loki and Tempo's objects, which are worth more than the cluster
// that wrote them.
//
// That is why this layer does not use pkg/layer. The shared runner resolves a
// cluster StackReference and builds a Kubernetes provider from its kubeconfig;
// this layer would then refuse to run until a cluster it does not need already
// existed.
//
// Hetzner has no API for Object Storage — no bucket resource in
// pulumi-hcloud, and no endpoint in the hcloud API. Buckets are S3 objects
// reached over the S3 protocol, so they are created by the AWS provider aimed
// at a Hetzner endpoint. The credentials are S3 credentials from the Hetzner
// Console, not an hcloud API token; the two are unrelated.
package main

import (
	"fmt"
	"strings"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/objectstorage"
	"github.com/oleg-tkachuk/hetzner-iac/pkg/pulumilog"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/s3"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

func main() {
	pulumi.Run(program)
}

// program is the layer, separated from main so the tests can run it against a
// mock monitor rather than re-implementing the wiring they are meant to check.
func program(ctx *pulumi.Context) error {
	cfg, err := readConfig(ctx)
	if err != nil {
		return err
	}

	credentials, err := readCredentials(ctx)
	if err != nil {
		return err
	}

	log := pulumilog.New(ctx)
	log.Step("endpoint", fmt.Sprintf("stack %s → %s", ctx.Stack(), cfg.EndpointHost()))

	provider, err := newProvider(ctx, cfg, credentials)
	if err != nil {
		return err
	}

	for _, bucket := range cfg.Buckets() {
		if err := create(ctx, cfg, provider, bucket); err != nil {
			return err
		}

		detail := bucket.Name
		if bucket.Versioning {
			detail = fmt.Sprintf("%s · versioned, %dd noncurrent",
				bucket.Name, cfg.RetainNoncurrentDays)
		}

		// Protected and retained is the property worth stating: it is why a
		// destroy of everything else leaves these standing.
		log.Done("bucket", detail+" · protected")
	}

	ctx.Export(objectstorage.OutputEndpointHost, pulumi.String(cfg.EndpointHost()))
	ctx.Export(objectstorage.OutputRegion, pulumi.String(cfg.Region()))

	// One output per bucket, so a consumer reads a name with StackReference's
	// typed accessors instead of asserting its way through a nested map.
	ctx.Export(objectstorage.OutputStateBucket, pulumi.String(cfg.BucketName(objectstorage.RoleState)))
	ctx.Export(objectstorage.OutputObservabilityBucket,
		pulumi.String(cfg.BucketName(objectstorage.RoleObservability)))

	// The whole PULUMI_BACKEND_URL, assembled. Every layer's Pulumi.yaml
	// documents this override and notes that the bucket has to exist first;
	// this is the value to paste once it does.
	ctx.Export(objectstorage.OutputStateBackendURL, pulumi.String(cfg.StateBackendURL()))

	return nil
}

// readConfig reads and validates the stack's configuration.
func readConfig(ctx *pulumi.Context) (*objectstorage.Config, error) {
	cfg := config.New(ctx, "object-storage")

	retain, err := objectstorage.ParseRetainNoncurrentDays(cfg.Get("retainNoncurrentDays"))
	if err != nil {
		return nil, err
	}

	out := &objectstorage.Config{
		Location:             cfg.Get("location"),
		NamePrefix:           cfg.Get("namePrefix"),
		RetainNoncurrentDays: retain,
	}

	if err := out.Validate(); err != nil {
		return nil, err
	}

	return out, nil
}

// credentials are the S3 credentials the provider signs with.
//
// Kept out of objectstorage.Config on purpose: that type is pure, comparable
// and safe to print in an error message, and it stops being any of those the
// moment it carries a secret.
type credentials struct {
	AccessKey pulumi.StringOutput
	SecretKey pulumi.StringOutput
}

// readCredentials reads the S3 credentials, reporting both missing keys at
// once.
//
// Not config.RequireSecret, which crashes the program with a stack trace. Every
// other misconfiguration in this layer produces one message naming what to
// fix, and a missing credential is the most likely of them on a first run.
func readCredentials(ctx *pulumi.Context) (*credentials, error) {
	cfg := config.New(ctx, "object-storage")

	var missing []string

	for _, key := range []string{"accessKey", "secretKey"} {
		// Presence only. The value itself is taken as a secret output below,
		// so it stays marked as one all the way into the provider.
		if _, err := cfg.Try(key); err != nil {
			missing = append(missing, key)
		}
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf(
			"S3 credentials are not configured: %s\n"+
				"These come from the Hetzner Console under the project's Object Storage,\n"+
				"and are not an hcloud API token:\n"+
				"  pulumi config set --secret object-storage:accessKey <key>\n"+
				"  pulumi config set --secret object-storage:secretKey <key>",
			strings.Join(missing, ", "))
	}

	return &credentials{
		AccessKey: cfg.GetSecret("accessKey"),
		SecretKey: cfg.GetSecret("secretKey"),
	}, nil
}

// newProvider aims the AWS provider at Hetzner.
func newProvider(
	ctx *pulumi.Context,
	cfg *objectstorage.Config,
	creds *credentials,
) (*aws.Provider, error) {
	return aws.NewProvider(ctx, "hetzner-object-storage", &aws.ProviderArgs{
		// S3 credentials from the Hetzner Console, under a project's Object
		// Storage. Not an hcloud API token — that token cannot sign an S3
		// request, and these credentials cannot create a server.
		AccessKey: creds.AccessKey,
		SecretKey: creds.SecretKey,

		// Hetzner uses the location as the SigV4 region. A mismatch between
		// this and the endpoint fails with a signature error naming neither.
		Region: pulumi.String(cfg.Region()),

		Endpoints: aws.ProviderEndpointArray{
			aws.ProviderEndpointArgs{S3: pulumi.String(cfg.Endpoint())},
		},

		// Path style, so a bucket is addressed as <endpoint>/<bucket> rather
		// than <bucket>.<endpoint>. Both work at Hetzner, but the creating
		// request is the awkward case for virtual-hosted addressing: it names
		// a host for a bucket that does not exist yet.
		S3UsePathStyle: pulumi.Bool(true),

		// Four AWS-only behaviours, switched off because there is no AWS here.
		// Left on, each one calls an endpoint Hetzner does not serve — STS to
		// check the credentials, STS again for the account id, the EC2
		// instance metadata service, and a hardcoded list of AWS regions that
		// fsn1 is not in.
		SkipCredentialsValidation: pulumi.Bool(true),
		SkipRequestingAccountId:   pulumi.Bool(true),
		SkipMetadataApiCheck:      pulumi.Bool(true),
		SkipRegionValidation:      pulumi.Bool(true),
	})
}

// create makes one bucket and the settings that belong to it.
func create(
	ctx *pulumi.Context,
	cfg *objectstorage.Config,
	provider *aws.Provider,
	spec objectstorage.Bucket,
) error {
	options := []pulumi.ResourceOption{
		pulumi.Provider(provider),

		// The point of the layer. Protect makes `pulumi destroy` refuse and
		// say so; RetainOnDelete leaves the bucket standing even if somebody
		// unprotects it and then removes it from the program. A bucket whose
		// whole purpose is to outlive the cluster should take two deliberate
		// steps to remove, not one.
		pulumi.Protect(true),
		pulumi.RetainOnDelete(true),
	}

	// Named by role key, not by bucket name: renaming namePrefix then renames
	// the bucket in place instead of replacing the resource — and replacing a
	// bucket means deleting one that, by design, refuses to be deleted.
	bucket, err := s3.NewBucket(ctx, spec.Key, &s3.BucketArgs{
		Bucket: pulumi.String(spec.Name),
	}, options...)
	if err != nil {
		return err
	}

	if !spec.Versioning {
		// No resource rather than an explicit Suspended: a bucket that never
		// had versioning is already in that state, and asking Hetzner to
		// suspend what was never enabled is a call worth not making.
		return nil
	}

	if _, versionErr := s3.NewBucketVersioning(ctx, spec.Key, &s3.BucketVersioningArgs{
		Bucket: bucket.Bucket,
		VersioningConfiguration: &s3.BucketVersioningVersioningConfigurationArgs{
			Status: pulumi.String("Enabled"),
		},
	}, pulumi.Provider(provider)); versionErr != nil {
		return versionErr
	}

	// Versioning without this is an unbounded bill. Hetzner implements exactly
	// one lifecycle rule — NoncurrentVersionExpiration, and only its
	// NoncurrentDays field — so this is not one option among several for
	// trimming superseded versions. It is the only one.
	_, err = s3.NewBucketLifecycleConfiguration(ctx, spec.Key, &s3.BucketLifecycleConfigurationArgs{
		Bucket: bucket.Bucket,
		Rules: s3.BucketLifecycleConfigurationRuleArray{
			s3.BucketLifecycleConfigurationRuleArgs{
				Id:     pulumi.String("expire-noncurrent-versions"),
				Status: pulumi.String("Enabled"),
				NoncurrentVersionExpiration: &s3.BucketLifecycleConfigurationRuleNoncurrentVersionExpirationArgs{
					NoncurrentDays: pulumi.Int(cfg.RetainNoncurrentDays),
				},
			},
		},
	}, pulumi.Provider(provider))

	return err
}
