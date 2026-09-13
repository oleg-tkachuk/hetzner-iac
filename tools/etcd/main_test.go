package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

// testConsistentIndex is what the fixture records as applied.
const testConsistentIndex = 38743

// testRevisions is how many entries the fixture's key bucket holds. More than
// one, so a count that reads the wrong bucket cannot pass by accident.
const testRevisions = 3

// fixture writes a bbolt database and returns its path.
//
// Built with bbolt rather than from crafted bytes: the point of this tool is
// that the library defines the format, so its tests have to be written in the
// same terms. omit names a bucket to leave out.
func fixture(t *testing.T, omit string) string {
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
		"no path":         {"verify"},
		"unknown command": {"restore", "db.snapshot"},
		"too many":        {"verify", "a", "b"},
	} {
		err := run(args)

		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "usage:", name)
	}
}

func TestRun_NamesAFileItCannotRead(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "nowhere.db")

	err := run([]string{"verify", missing})

	require.Error(t, err)
	assert.Contains(t, err.Error(), missing)
}

func TestRun_RefusesAFileThatIsNotABoltDatabase(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "not.db")
	require.NoError(t, os.WriteFile(path, []byte("a text file with the right extension"), 0o600))

	err := run([]string{"verify", path})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not an etcd snapshot")
}

func TestRun_AcceptsASnapshotShapedDatabase(t *testing.T) {
	t.Parallel()

	require.NoError(t, run([]string{"verify", fixture(t, "")}))
}
