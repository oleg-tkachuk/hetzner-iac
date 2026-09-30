package main

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
)

// testConsistentIndex is what the fixture records as applied.
const testConsistentIndex = 38743

// testRevisions is how many entries the fixture's key bucket holds. More than
// one, so a count that reads the wrong bucket cannot pass by accident.
const testRevisions = 3

// testCluster is the cluster the fixture's members belong to.
const testCluster = "platform-dev"

// ownMembers are the fixture's etcd members by default: the control plane of
// testCluster, named the way Talos names them.
func ownMembers() []string {
	return []string{
		clusterspec.NodeName(testCluster, clusterspec.RoleControlPlane, 0),
		clusterspec.NodeName(testCluster, clusterspec.RoleControlPlane, 1),
		clusterspec.NodeName(testCluster, clusterspec.RoleControlPlane, 2),
	}
}

// storedMember is a member as etcd writes it into the members bucket, copied
// from a real snapshot of this platform.
func storedMember(t *testing.T, id int, name string) []byte {
	t.Helper()

	raw, err := json.Marshal(map[string]any{
		"id":         id,
		"peerURLs":   []string{"https://10.0.1.2:2380"},
		"name":       name,
		"clientURLs": []string{"https://10.0.1.2:2379"},
	})
	require.NoError(t, err)

	return raw
}

// fixture writes a bbolt database holding ownMembers and returns its path.
func fixture(t *testing.T, omit string) string {
	t.Helper()

	return fixtureWith(t, omit, ownMembers())
}

// fixtureWith writes a bbolt database and returns its path.
//
// Built with bbolt rather than from crafted bytes: the point of this tool is
// that the library defines the format, so its tests have to be written in the
// same terms. omit names a bucket to leave out.
func fixtureWith(t *testing.T, omit string, members []string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "db.snapshot")

	db, err := bolt.Open(path, 0o600, nil)
	require.NoError(t, err)

	require.NoError(t, db.Update(func(tx *bolt.Tx) error {
		for _, name := range requiredBuckets {
			if name == omit {
				continue
			}

			bucket, createErr := tx.CreateBucket([]byte(name))
			if createErr != nil {
				return createErr
			}

			switch name {
			case keyBucket:
				for i := range testRevisions {
					if putErr := bucket.Put([]byte{byte(i)}, []byte("v")); putErr != nil {
						return putErr
					}
				}

			case metaBucket:
				applied := make([]byte, 8)
				binary.BigEndian.PutUint64(applied, testConsistentIndex)

				if putErr := bucket.Put([]byte(consistentIndexKey), applied); putErr != nil {
					return putErr
				}

			case membersBucket:
				for i, name := range members {
					if putErr := bucket.Put([]byte(strconv.Itoa(i)), storedMember(t, i, name)); putErr != nil {
						return putErr
					}
				}
			}
		}

		return nil
	}))
	require.NoError(t, db.Close())

	return path
}

// open reopens a fixture read-only, the way run does.
func open(t *testing.T, path string) *bolt.DB {
	t.Helper()

	db, err := bolt.Open(path, fileMode, &bolt.Options{ReadOnly: true, Timeout: openTimeout})
	require.NoError(t, err)

	t.Cleanup(func() { _ = db.Close() })

	return db
}

func TestInspect_ReadsWhatASnapshotSaysAboutItself(t *testing.T) {
	t.Parallel()

	facts, err := inspect(open(t, fixture(t, "")))

	require.NoError(t, err)
	assert.Equal(t, testRevisions, facts.Revisions)
	assert.Equal(t, uint64(testConsistentIndex), facts.ConsistentIndex,
		"the consistent index is the one number that says whether this is the snapshot expected")
	assert.ElementsMatch(t, ownMembers(), facts.Members)
}

func TestInspect_SkipsAMemberThatNeverStarted(t *testing.T) {
	t.Parallel()

	facts, err := inspect(open(t, fixtureWith(t, "", append(ownMembers(), ""))))

	require.NoError(t, err)
	assert.ElementsMatch(t, ownMembers(), facts.Members)
}

func TestInspect_RefusesAMembersEntryThatIsNotAMember(t *testing.T) {
	t.Parallel()

	path := fixture(t, "")

	db, err := bolt.Open(path, 0o600, nil)
	require.NoError(t, err)
	require.NoError(t, db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(membersBucket)).Put([]byte("x"), []byte("not json"))
	}))
	require.NoError(t, db.Close())

	_, err = inspect(open(t, path))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not an etcd member")
}

// TestBelongsTo is the bug this exists for: a dev snapshot restored or
// uploaded under stack=prod. Nothing in the file name said which stack took
// it, and upload chose the newest snapshot of any.
func TestBelongsTo(t *testing.T) {
	t.Parallel()

	node := func(cluster string, ordinal int) string {
		return clusterspec.NodeName(cluster, clusterspec.RoleControlPlane, ordinal)
	}

	for _, tc := range []struct {
		name    string
		members []string
		wantErr string
	}{
		{"its own control plane", ownMembers(), ""},
		{"a single node", []string{node(testCluster, 0)}, ""},
		{"another cluster's", []string{node("platform-prod", 0), node("platform-prod", 1)}, "platform-prod-control-plane-0"},
		{"one foreign member among its own", append(ownMembers(), node("platform-prod", 3)), "platform-prod-control-plane-3"},
		{"a cluster whose name extends this one", []string{node(testCluster+"2", 0)}, "another cluster"},
		{"a worker's name", []string{clusterspec.NodeName(testCluster, "general", 0)}, "another cluster"},
		{"no ordinal", []string{clusterspec.NodeNamePrefix(testCluster, clusterspec.RoleControlPlane) + "x"}, "another cluster"},
		{"no named member", nil, "no named etcd member"},
	} {
		err := belongsTo(tc.members, testCluster)

		if tc.wantErr == "" {
			assert.NoError(t, err, tc.name)

			continue
		}

		if assert.Error(t, err, tc.name) {
			assert.Contains(t, err.Error(), tc.wantErr, tc.name)
		}
	}
}

func TestRun_VerifiesTheSnapshotAgainstTheCluster(t *testing.T) {
	t.Parallel()

	require.NoError(t, run([]string{"verify", "--cluster", testCluster, fixture(t, "")}))

	err := run([]string{"verify", "--cluster", "platform-prod", fixture(t, "")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "taken from another cluster")
}

func TestInspect_RefusesADatabaseMissingAnyBucketASnapshotHas(t *testing.T) {
	t.Parallel()

	// The case the hand-written header reader could not see at all: a bbolt
	// database that opens perfectly and is somebody else's.
	for _, missing := range requiredBuckets {
		_, err := inspect(open(t, fixture(t, missing)))

		require.Error(t, err, missing)
		assert.Contains(t, err.Error(), missing)
		assert.Contains(t, err.Error(), "not an etcd snapshot", missing)
	}
}

func TestInspect_RefusesAConsistentIndexThatIsNotOne(t *testing.T) {
	t.Parallel()

	path := fixture(t, "")

	db, err := bolt.Open(path, 0o600, nil)
	require.NoError(t, err)
	require.NoError(t, db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(metaBucket)).Put([]byte(consistentIndexKey), []byte("short"))
	}))
	require.NoError(t, db.Close())

	// Read as a number anyway, five bytes would decode to something plausible
	// and wrong.
	_, err = inspect(open(t, path))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "want 8")
}

func TestRun_RejectsAnythingButVerify(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"none":            {},
		"no path":         {"verify", "--cluster", testCluster},
		"no cluster":      {"verify", "db.snapshot"},
		"empty cluster":   {"verify", "--cluster", "", "db.snapshot"},
		"unknown flag":    {"verify", "--stack", "dev", "db.snapshot"},
		"unknown command": {"restore", "--cluster", testCluster, "db.snapshot"},
		"too many":        {"verify", "--cluster", testCluster, "a", "b"},
	} {
		err := run(args)

		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "usage:", name)
	}
}

func TestRun_NamesAFileItCannotRead(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "nowhere.db")

	err := run([]string{"verify", "--cluster", testCluster, missing})

	require.Error(t, err)
	assert.Contains(t, err.Error(), missing)
}

func TestRun_RefusesAFileThatIsNotABoltDatabase(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "not.db")
	require.NoError(t, os.WriteFile(path, []byte("a text file with the right extension"), 0o600))

	err := run([]string{"verify", "--cluster", testCluster, path})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not an etcd snapshot")
}

func TestRun_AcceptsASnapshotShapedDatabase(t *testing.T) {
	t.Parallel()

	require.NoError(t, run([]string{"verify", "--cluster", testCluster, fixture(t, "")}))
}

// corruptLeaf writes a snapshot big enough to need several data pages, then
// overwrites the page id in the header of one leaf page the key bucket uses —
// the kind of damage that leaves the meta pages, and so bbolt.Open, content.
func corruptLeaf(t *testing.T) string {
	t.Helper()

	path := fixture(t, "")

	db, err := bolt.Open(path, 0o600, nil)
	require.NoError(t, err)

	require.NoError(t, db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(keyBucket))

		for i := range manyRevisions {
			if putErr := bucket.Put([]byte(strconv.Itoa(i)), make([]byte, revisionSize)); putErr != nil {
				return putErr
			}
		}

		return nil
	}))

	pageSize := int64(db.Info().PageSize)
	leaf := int64(-1)

	require.NoError(t, db.View(func(tx *bolt.Tx) error {
		for id := range scannedPages {
			if page, pageErr := tx.Page(id); pageErr == nil && page != nil && page.Type == "leaf" {
				leaf = int64(id)
			}
		}

		return nil
	}))
	require.NoError(t, db.Close())
	require.Positive(t, leaf, "the fixture has no leaf page to damage")

	file, err := os.OpenFile(path, os.O_RDWR, 0)
	require.NoError(t, err)

	wrongID := make([]byte, 8)
	binary.LittleEndian.PutUint64(wrongID, uint64(leaf)+1)

	_, err = file.WriteAt(wrongID, leaf*pageSize)
	require.NoError(t, err)
	require.NoError(t, file.Close())

	return path
}

// Enough revisions of this size to spread the key bucket over several pages,
// and how many page ids to look through for one of them.
const (
	manyRevisions = 2000
	revisionSize  = 100
	scannedPages  = 64
)

// TestInspect_RefusesADamagedDataPage: opening a snapshot validates only its
// meta pages, and a damaged data page opened fine and then crashed the reader
// — or, in a bucket this tool does not read, was never looked at. bbolt's own
// consistency check walks every page, and runs before anything reads one.
func TestInspect_RefusesADamagedDataPage(t *testing.T) {
	t.Parallel()

	_, err := inspect(open(t, corruptLeaf(t)))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "consistency")
}
