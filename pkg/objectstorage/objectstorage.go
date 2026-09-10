// Package objectstorage describes the buckets the 05-object-storage layer
// creates in Hetzner Object Storage.
//
// The layer exists because two things in this repository need durable storage
// with a lifecycle longer than the cluster's:
//
//   - Pulumi state, when the backend is Hetzner rather than Pulumi Cloud. Every
//     layer's Pulumi.yaml documents the override and notes the catch: "a bucket
//     managed by the state it holds cannot create itself".
//   - Loki and Tempo, which write to object storage natively. A persistent
//     volume is the fallback, not the destination.
//
// Hetzner Object Storage is S3-compatible only in part, and the gaps shape what
// is buildable here. Verified against their published list of supported
// actions: versioning works; lifecycle offers exactly one rule,
// NoncurrentVersionExpiration with NoncurrentDays; tagging, replication,
// notifications and bucket policies do not exist, and encryption is SSE-C only.
//
// The absence of replication is the one to remember: a second copy in another
// location is the backup tool's job, never the bucket's.
package objectstorage

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Output names this layer exports. Constants rather than literals for the
// same reason the cluster tier's are: renaming one is a breaking change to
// every stack that reads it.
const (
	OutputEndpoint        = "endpoint"
	OutputRegion          = "region"
	OutputBuckets         = "buckets"
	OutputStateBackendURL = "stateBackendUrl"
)

// Locations that host Object Storage, mapped to the region string SigV4 signs
// with. Hetzner uses the location as the region, so the two are the same
// value — kept as a map anyway, because "which locations exist" is the
// question this answers, and it is not the same set as the locations that host
// servers.
var locations = map[string]string{
	"fsn1": "fsn1", // Falkenstein
	"nbg1": "nbg1", // Nuremberg
	"hel1": "hel1", // Helsinki
}

// endpointSuffix is the host every location's endpoint ends with.
const endpointSuffix = "your-objectstorage.com"

// bucketName is the subset of S3 naming that is also a DNS label, because
// Hetzner addresses buckets as <bucket>.<location>.your-objectstorage.com.
var bucketName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// Role is what a bucket is for. Buckets are not interchangeable: what belongs
// in one is not what belongs in the other, and the settings differ because the
// access patterns do.
type Role struct {
	// Suffix is appended to the configured prefix to name the bucket.
	Suffix string

	// Purpose is carried into the stack's outputs, so an operator reading
	// `pulumi stack output` learns what a bucket is without reading this file.
	Purpose string

	// Versioning keeps every overwrite of an object as a recoverable
	// version. It is not free: a versioned bucket holds every superseded
	// object until the noncurrent-expiry rule removes it.
	Versioning bool
}

// RoleState is the state bucket's key. Named because StateBackendURL looks it
// up, and a typo there would silently return an empty URL.
const RoleState = "state"

// RoleObservability is the key of the bucket Loki and Tempo write to.
const RoleObservability = "observability"

// roles is the whole set. Ordered by returning a sorted slice rather than
// ranging the map, so the resource names Pulumi records do not move between
// runs — a map's iteration order would make every second preview a diff.
var roles = map[string]Role{
	RoleState: {
		Suffix:  RoleState,
		Purpose: "pulumi state, when PULUMI_BACKEND_URL points here instead of at Pulumi Cloud",
		// State is the one thing in this repository that cannot be rebuilt
		// from the tree. An update that corrupts it is recoverable only from
		// the version before it.
		Versioning: true,
	},
	RoleObservability: {
		Suffix:  RoleObservability,
		Purpose: "loki chunks and tempo blocks",
		// Off deliberately. Loki and Tempo write immutable objects and delete
		// them when retention expires; versioning would keep a copy of every
		// deleted chunk, which is storage nobody ever reads and a bill that
		// grows with the delete rate rather than the data.
		Versioning: false,
	},
}

// Config is what the layer's stack configuration deserialises into.
type Config struct {
	// Location is where the buckets live: fsn1, nbg1 or hel1.
	Location string

	// NamePrefix distinguishes this installation's buckets from every other
	// one in the account, and from every other Hetzner customer's — bucket
	// names share a DNS namespace, so they collide globally, not per project.
	NamePrefix string

	// RetainNoncurrentDays is how long a superseded version survives in a
	// versioned bucket.
	//
	// It has no default of "off": Hetzner's only lifecycle rule is
	// NoncurrentVersionExpiration, so a versioned bucket without this grows
	// without bound and nothing else will ever trim it.
	RetainNoncurrentDays int
}

// DefaultRetainNoncurrentDays is long enough to notice a bad state update and
// roll back from a version, short enough that superseded state is not a
// storage line item.
const DefaultRetainNoncurrentDays = 30

// ParseRetainNoncurrentDays turns the raw stack-config value into days.
//
// An empty string means unset, and takes the default. An explicit value is
// parsed and returned as it is — including a zero or a negative, which
// Validate then rejects with the reason. Substituting the default for a value
// the operator actually wrote would hide the mistake rather than report it.
func ParseRetainNoncurrentDays(raw string) (int, error) {
	if raw == "" {
		return DefaultRetainNoncurrentDays, nil
	}

	days, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("retainNoncurrentDays %q is not a number of days: %w", raw, err)
	}

	return days, nil
}

// longestSuffix is the room a prefix has to leave for the name it will carry.
func longestSuffix() int {
	longest := 0
	for _, role := range roles {
		if n := len(role.Suffix); n > longest {
			longest = n
		}
	}

	return longest
}

// maxBucketName is the S3 limit, and also the DNS-label limit that Hetzner's
// virtual-hosted addressing runs into first.
const maxBucketName = 63

// Validate reports every problem it finds rather than the first, for the same
// reason the cluster topology does: this configuration is written once, by
// hand, and one error per run turns three mistakes into three round trips.
func (c *Config) Validate() error {
	var problems []string

	if _, known := locations[c.Location]; !known {
		problems = append(problems, fmt.Sprintf(
			"location %q does not host Object Storage (%s)",
			c.Location, strings.Join(sortedLocations(), ", ")))
	}

	problems = append(problems, c.validatePrefix()...)

	if c.RetainNoncurrentDays < 1 {
		problems = append(problems, fmt.Sprintf(
			"retainNoncurrentDays is %d: a versioned bucket keeps every superseded object, "+
				"and NoncurrentVersionExpiration is the only lifecycle rule Hetzner implements",
			c.RetainNoncurrentDays))
	}

	if len(problems) == 0 {
		return nil
	}

	return fmt.Errorf("invalid object storage configuration:\n  - %s", strings.Join(problems, "\n  - "))
}

func (c *Config) validatePrefix() []string {
	var problems []string

	room := maxBucketName - longestSuffix() - 1 // the joining hyphen

	switch {
	case c.NamePrefix == "":
		problems = append(problems, "namePrefix is required: bucket names share one DNS namespace "+
			"across all of Hetzner, so an unprefixed name collides with another customer's")
	case len(c.NamePrefix) > room:
		problems = append(problems, fmt.Sprintf(
			"namePrefix %q is %d characters, leaving no room for the longest bucket suffix (max %d)",
			c.NamePrefix, len(c.NamePrefix), room))
	case !bucketName.MatchString(c.NamePrefix):
		problems = append(problems, fmt.Sprintf(
			"namePrefix %q must be lowercase alphanumeric with internal hyphens: it becomes a DNS label",
			c.NamePrefix))
	}

	return problems
}

// Endpoint is the S3 endpoint for the configured location.
func (c *Config) Endpoint() string {
	return fmt.Sprintf("https://%s.%s", c.Location, endpointSuffix)
}

// Region is what SigV4 signs with. Hetzner uses the location.
func (c *Config) Region() string {
	return locations[c.Location]
}

// Bucket is one bucket, resolved: a role with its name filled in.
type Bucket struct {
	Role

	// Key is the role's key, used as the Pulumi resource name. Stable across
	// runs and independent of NamePrefix, so renaming the prefix renames the
	// bucket without replacing the resource under it.
	Key string

	// Name is what the bucket is actually called.
	Name string
}

// Buckets resolves every role against the configuration, in a stable order.
func (c *Config) Buckets() []Bucket {
	out := make([]Bucket, 0, len(roles))

	for _, key := range sortedRoles() {
		role := roles[key]
		out = append(out, Bucket{
			Role: role,
			Key:  key,
			Name: c.NamePrefix + "-" + role.Suffix,
		})
	}

	return out
}

// StateBackendURL is the PULUMI_BACKEND_URL for the state bucket.
//
// Composed here rather than left to the operator because the query string is
// three settings that all have to be right at once, and getting one wrong
// produces an authentication failure that names none of them.
func (c *Config) StateBackendURL() string {
	for _, bucket := range c.Buckets() {
		if bucket.Key != RoleState {
			continue
		}

		return fmt.Sprintf("s3://%s?endpoint=%s.%s&s3ForcePathStyle=true&region=%s",
			bucket.Name, c.Location, endpointSuffix, c.Region())
	}

	return ""
}

func sortedLocations() []string {
	out := make([]string, 0, len(locations))
	for name := range locations {
		out = append(out, name)
	}

	sort.Strings(out)

	return out
}

func sortedRoles() []string {
	out := make([]string, 0, len(roles))
	for key := range roles {
		out = append(out, key)
	}

	sort.Strings(out)

	return out
}
