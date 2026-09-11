package main

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testProject = "object-storage"
	testStack   = "test"

	bucketType     = "aws:s3/bucket:Bucket"
	versioningType = "aws:s3/bucketVersioning:BucketVersioning"
	lifecycleType  = "aws:s3/bucketLifecycleConfiguration:BucketLifecycleConfiguration"
	providerType   = "pulumi:providers:aws"
)

// mocks records every registered resource so a test can assert on what the
// program asked for.
type mocks struct {
	mu        sync.Mutex
	resources map[string][]resource.PropertyMap
}

func newMocks() *mocks {
	return &mocks{resources: map[string][]resource.PropertyMap{}}
}

func (m *mocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	m.mu.Lock()
	m.resources[args.TypeToken] = append(m.resources[args.TypeToken], args.Inputs)
	m.mu.Unlock()

	return args.Name, args.Inputs, nil
}

func (m *mocks) Call(pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

func (m *mocks) of(token string) []resource.PropertyMap {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.resources[token]
}

// setConfig writes the stack configuration the program reads. Config reaches a
// Pulumi program through PULUMI_CONFIG, and t.Setenv forbids t.Parallel, which
// is why nothing here runs in parallel.
func setConfig(t *testing.T, overrides map[string]string) {
	t.Helper()

	values := map[string]string{
		testProject + ":location":   "fsn1",
		testProject + ":namePrefix": "platform-prod",
		testProject + ":accessKey":  "AKIAEXAMPLE",
		testProject + ":secretKey":  "not-a-real-secret",
	}

	for key, value := range overrides {
		if value == "" {
			delete(values, testProject+":"+key)
			continue
		}

		values[testProject+":"+key] = value
	}

	raw, err := json.Marshal(values)
	require.NoError(t, err)

	t.Setenv("PULUMI_CONFIG", string(raw))
}

func run(t *testing.T, m *mocks) error {
	t.Helper()

	return pulumi.RunErr(program, pulumi.WithMocks(testProject, testStack, m))
}

func TestProgram_CreatesBothBuckets(t *testing.T) {
	setConfig(t, nil)

	m := newMocks()
	require.NoError(t, run(t, m))

	buckets := m.of(bucketType)
	require.Len(t, buckets, 2)

	names := map[string]bool{}
	for _, bucket := range buckets {
		names[bucket["bucket"].StringValue()] = true
	}

	// Names are prefix + role, so one prefix change moves both buckets and
	// neither can collide with another installation's.
	assert.Equal(t, map[string]bool{
		"platform-prod-state":         true,
		"platform-prod-observability": true,
	}, names)
}

func TestProgram_VersionsStateOnlyAndTrimsIt(t *testing.T) {
	setConfig(t, nil)

	m := newMocks()
	require.NoError(t, run(t, m))

	// One versioning resource, not two: chunks that retention deletes should
	// not leave a version behind for every delete.
	versioning := m.of(versioningType)
	require.Len(t, versioning, 1)
	assert.Equal(t, "platform-prod-state", versioning[0]["bucket"].StringValue())
	assert.Equal(t, "Enabled",
		versioning[0]["versioningConfiguration"].ObjectValue()["status"].StringValue())

	// And exactly one lifecycle configuration, on the same bucket. Versioning
	// without it is a bucket that only grows: NoncurrentVersionExpiration is
	// the only lifecycle rule Hetzner implements.
	lifecycle := m.of(lifecycleType)
	require.Len(t, lifecycle, 1)
	assert.Equal(t, "platform-prod-state", lifecycle[0]["bucket"].StringValue())

	rules := lifecycle[0]["rules"].ArrayValue()
	require.Len(t, rules, 1)

	rule := rules[0].ObjectValue()
	assert.Equal(t, "Enabled", rule["status"].StringValue())
	assert.EqualValues(t, 30,
		rule["noncurrentVersionExpiration"].ObjectValue()["noncurrentDays"].NumberValue())
}

func TestProgram_HonoursAnExplicitRetention(t *testing.T) {
	setConfig(t, map[string]string{"retainNoncurrentDays": "7"})

	m := newMocks()
	require.NoError(t, run(t, m))

	rule := m.of(lifecycleType)[0]["rules"].ArrayValue()[0].ObjectValue()
	assert.EqualValues(t, 7,
		rule["noncurrentVersionExpiration"].ObjectValue()["noncurrentDays"].NumberValue())
}

func TestProgram_AimsTheAwsProviderAtHetzner(t *testing.T) {
	setConfig(t, map[string]string{"location": "hel1"})

	m := newMocks()
	require.NoError(t, run(t, m))

	providers := m.of(providerType)
	require.Len(t, providers, 1)

	provider := providers[0]

	// Endpoint and region have to agree, or SigV4 fails with a signature
	// error that names neither of them.
	assert.Equal(t, "https://hel1.your-objectstorage.com",
		provider["endpoints"].ArrayValue()[0].ObjectValue()["s3"].StringValue())
	assert.Equal(t, "hel1", provider["region"].StringValue())

	// Path style, and the four AWS-only calls switched off. Any of them left
	// on reaches for an endpoint Hetzner does not serve.
	for _, key := range []resource.PropertyKey{
		"s3UsePathStyle",
		"skipCredentialsValidation",
		"skipRequestingAccountId",
		"skipMetadataApiCheck",
		"skipRegionValidation",
	} {
		require.Contains(t, provider, key)
		assert.True(t, provider[key].BoolValue(), string(key))
	}
}

func TestProgram_RejectsALocationWithoutObjectStorage(t *testing.T) {
	setConfig(t, map[string]string{"location": "ash"})

	// Before any resource is registered: a location that cannot host a bucket
	// should fail here, not against the S3 endpoint of a host that does not
	// resolve.
	m := newMocks()
	err := run(t, m)

	require.Error(t, err)
	assert.Contains(t, err.Error(), `location "ash"`)
	assert.Empty(t, m.of(bucketType))
}

func TestProgram_RejectsAMissingNamePrefix(t *testing.T) {
	setConfig(t, map[string]string{"namePrefix": ""})

	m := newMocks()
	err := run(t, m)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "namePrefix is required")
	assert.Empty(t, m.of(bucketType))
}

func TestProgram_RejectsRetentionThatIsNotANumber(t *testing.T) {
	setConfig(t, map[string]string{"retainNoncurrentDays": "thirty"})

	m := newMocks()
	err := run(t, m)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "retainNoncurrentDays")
	assert.Empty(t, m.of(bucketType))
}

func TestProgram_RequiresTheS3Credentials(t *testing.T) {
	// Configuration valid apart from the credentials. The provider cannot
	// sign a request without them, and the failure should say which key is
	// missing rather than surface as an authentication error later.
	for _, key := range []string{"accessKey", "secretKey"} {
		setConfig(t, map[string]string{key: ""})

		err := run(t, newMocks())

		require.Error(t, err, key)
		assert.Contains(t, err.Error(), key)
	}
}

// ---------------------------------------------------------------------------
// Opt-in
// ---------------------------------------------------------------------------

func TestProgram_NoCredentialsCreatesNothingAndSucceeds(t *testing.T) {
	// `task platform:plan-all` walks every layer. A stack that was never given
	// S3 credentials has to produce no buckets and no error, the way 30-core
	// produces no ClusterIssuer without acmeEmail — otherwise one unconfigured
	// optional layer stops the walk, which is how this was found.
	setConfig(t, map[string]string{
		"accessKey":  "",
		"secretKey":  "",
		"location":   "",
		"namePrefix": "",
	})

	m := newMocks()
	require.NoError(t, run(t, m))

	assert.Empty(t, m.of(bucketType))
	assert.Empty(t, m.of(providerType))
}

func TestProgram_OneCredentialIsAMistakeNotAnOptOut(t *testing.T) {
	// Half-configured means the operator meant to turn this on and stopped.
	// Skipping silently would leave them reading an empty preview and looking
	// for the bucket in the Console.
	for name, override := range map[string]map[string]string{
		"no secret key": {"secretKey": ""},
		"no access key": {"accessKey": ""},
	} {
		setConfig(t, override)

		err := run(t, newMocks())
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "S3 credentials are not configured", name)
	}
}

func TestRequested_TracksOnlyTheCredentials(t *testing.T) {
	// The switch is the credentials, not the other keys: those carry
	// `default: ""` in Pulumi.yaml to stop Pulumi's own validation demanding
	// them, so they always arrive set and can say nothing about intent.
	for name, tc := range map[string]struct {
		overrides map[string]string
		want      bool
	}{
		"both":          {nil, true},
		"access key":    {map[string]string{"secretKey": ""}, true},
		"secret key":    {map[string]string{"accessKey": ""}, true},
		"neither":       {map[string]string{"accessKey": "", "secretKey": ""}, false},
		"only location": {map[string]string{"accessKey": "", "secretKey": "", "namePrefix": ""}, false},
	} {
		setConfig(t, tc.overrides)

		var got bool

		require.NoError(t, pulumi.RunErr(func(ctx *pulumi.Context) error {
			got = requested(ctx)

			return nil
		}, pulumi.WithMocks(testProject, testStack, newMocks())), name)

		assert.Equal(t, tc.want, got, name)
	}
}
