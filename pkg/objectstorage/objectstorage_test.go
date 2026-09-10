package objectstorage_test

import (
	"strings"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/objectstorage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// valid is the configuration each test starts from and mutates one field of,
// so a test names the one thing it is about.
func valid() *objectstorage.Config {
	return &objectstorage.Config{
		Location:             "fsn1",
		NamePrefix:           "platform-prod",
		RetainNoncurrentDays: objectstorage.DefaultRetainNoncurrentDays,
	}
}

func TestValidate_AcceptsEveryObjectStorageLocation(t *testing.T) {
	t.Parallel()

	// The set is smaller than the set of locations that host servers: a
	// cluster in a location without Object Storage is legitimate, and this
	// layer has to say so rather than fail at the API.
	for _, location := range []string{"fsn1", "nbg1", "hel1"} {
		cfg := valid()
		cfg.Location = location

		assert.NoError(t, cfg.Validate(), location)
	}
}

func TestValidate_RejectsALocationWithoutObjectStorage(t *testing.T) {
	t.Parallel()

	cfg := valid()
	cfg.Location = "ash" // Ashburn hosts servers, not Object Storage.

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `location "ash"`)
	// The message lists the alternatives, because the next thing the operator
	// needs is which ones are valid.
	assert.Contains(t, err.Error(), "fsn1")
}

func TestValidate_ReportsEveryProblemAtOnce(t *testing.T) {
	t.Parallel()

	// Three mistakes in one file should cost one round trip, not three.
	cfg := &objectstorage.Config{
		Location:             "nowhere",
		NamePrefix:           "Platform_Prod",
		RetainNoncurrentDays: 0,
	}

	err := cfg.Validate()
	require.Error(t, err)

	problems := strings.Count(err.Error(), "\n  - ")
	assert.Equal(t, 3, problems, "expected all three problems, got:\n%s", err)
}

func TestValidate_RequiresANamePrefix(t *testing.T) {
	t.Parallel()

	// Bucket names collide across all of Hetzner, not within one project, so
	// an unprefixed name is not merely untidy — it is unavailable.
	cfg := valid()
	cfg.NamePrefix = ""

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "namePrefix is required")
}

func TestValidate_RejectsAPrefixThatIsNotADNSLabel(t *testing.T) {
	t.Parallel()

	for name, prefix := range map[string]string{
		"uppercase":       "Platform",
		"underscore":      "platform_prod",
		"leading hyphen":  "-platform",
		"trailing hyphen": "platform-",
		"dot":             "platform.prod",
	} {
		cfg := valid()
		cfg.NamePrefix = prefix

		err := cfg.Validate()
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "DNS label", name)
	}
}

func TestValidate_RejectsAPrefixWithNoRoomForTheSuffix(t *testing.T) {
	t.Parallel()

	// 63 is the S3 limit and the DNS-label limit both. A prefix that fits on
	// its own but not with "-observability" appended has to fail here, not at
	// the API on the second of two buckets — one created, one not.
	cfg := valid()
	cfg.NamePrefix = strings.Repeat("a", 50)

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no room for the longest bucket suffix")

	// One shorter fits: 49 + "-observability" is exactly 63.
	cfg.NamePrefix = strings.Repeat("a", 49)
	require.NoError(t, cfg.Validate())

	for _, bucket := range cfg.Buckets() {
		assert.LessOrEqual(t, len(bucket.Name), 63, bucket.Name)
	}
}

func TestValidate_RejectsRetentionThatNothingWouldEverTrim(t *testing.T) {
	t.Parallel()

	// NoncurrentVersionExpiration is the only lifecycle rule Hetzner
	// implements. Zero here is not "keep forever by choice", it is a versioned
	// bucket with no way to shrink.
	for _, days := range []int{0, -1} {
		cfg := valid()
		cfg.RetainNoncurrentDays = days

		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "retainNoncurrentDays")
	}
}

func TestEndpointAndRegion(t *testing.T) {
	t.Parallel()

	cfg := valid()

	// Both are settings a client has to get right together; a mismatch
	// between region and endpoint fails SigV4 with a signature error that
	// mentions neither.
	assert.Equal(t, "https://fsn1.your-objectstorage.com", cfg.Endpoint())
	assert.Equal(t, "fsn1", cfg.Region())

	// The exported form carries no scheme: Loki and Tempo prepend their own,
	// and a scheme here reaches them as https://https://…
	assert.Equal(t, "fsn1.your-objectstorage.com", cfg.EndpointHost())
}

func TestBuckets_AreStableAndDistinct(t *testing.T) {
	t.Parallel()

	cfg := valid()

	first := cfg.Buckets()
	require.Len(t, first, 2)

	// Resource names come from the keys. If the order moved between runs,
	// every other preview would report a diff for resources that had not
	// changed.
	assert.Equal(t, first, cfg.Buckets())

	names := map[string]bool{}

	for _, bucket := range first {
		assert.False(t, names[bucket.Name], "duplicate bucket name %q", bucket.Name)
		names[bucket.Name] = true
		assert.True(t, strings.HasPrefix(bucket.Name, cfg.NamePrefix+"-"), bucket.Name)
	}
}

func TestBuckets_VersionStateAndNotChunks(t *testing.T) {
	t.Parallel()

	// The two roles differ on purpose, and the reason is asymmetric: state
	// cannot be rebuilt from the tree, so a superseded version is the only way
	// back; Loki and Tempo chunks are deleted by retention, and keeping a
	// version of each deleted chunk is cost with no reader.
	byKey := map[string]objectstorage.Bucket{}
	for _, bucket := range valid().Buckets() {
		byKey[bucket.Key] = bucket
	}

	require.Contains(t, byKey, "state")
	require.Contains(t, byKey, "observability")

	assert.True(t, byKey["state"].Versioning)
	assert.False(t, byKey["observability"].Versioning)
}

func TestStateBackendURL_CarriesAllThreeSettings(t *testing.T) {
	t.Parallel()

	// Exported so the operator copies it rather than composing it: endpoint,
	// path style and region all have to be right at once, and getting one
	// wrong produces a failure that names none of them.
	url := valid().StateBackendURL()

	assert.Equal(t,
		"s3://platform-prod-state?endpoint=fsn1.your-objectstorage.com&s3ForcePathStyle=true&region=fsn1",
		url)
}

func TestOutputNames_ArePinned(t *testing.T) {
	t.Parallel()

	// These are a wire contract with anything that reads this stack. A rename
	// is a breaking change, so it should break a test here rather than a
	// consumer's StackReference at apply time.
	assert.Equal(t, "endpointHost", objectstorage.OutputEndpointHost)
	assert.Equal(t, "region", objectstorage.OutputRegion)
	assert.Equal(t, "stateBucket", objectstorage.OutputStateBucket)
	assert.Equal(t, "observabilityBucket", objectstorage.OutputObservabilityBucket)
	assert.Equal(t, "stateBackendUrl", objectstorage.OutputStateBackendURL)
}

func TestBucketName_AgreesWithBuckets(t *testing.T) {
	t.Parallel()

	cfg := valid()

	// Two callers build a name: the export, one role at a time, and the loop
	// that creates them. They must not disagree — an export naming a bucket
	// that was never created is a consumer pointed at nothing.
	for _, bucket := range cfg.Buckets() {
		assert.Equal(t, bucket.Name, cfg.BucketName(bucket.Key), bucket.Key)
	}

	assert.Equal(t, "platform-prod-state", cfg.BucketName(objectstorage.RoleState))
	assert.Equal(t, "platform-prod-observability", cfg.BucketName(objectstorage.RoleObservability))
}

func TestBucketName_IsEmptyForAnUnknownRole(t *testing.T) {
	t.Parallel()

	// Rather than a plausible-looking name for a bucket that does not exist.
	assert.Empty(t, valid().BucketName("backups"))
}
