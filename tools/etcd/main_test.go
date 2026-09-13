package main

import (
	"encoding/binary"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// metaPage builds a bbolt meta page with the fields this checks.
func metaPage(magic, version, pageSize uint32) []byte {
	page := make([]byte, headerBytes)

	binary.LittleEndian.PutUint32(page[magicOffset:], magic)
	binary.LittleEndian.PutUint32(page[versionOffset:], version)
	binary.LittleEndian.PutUint32(page[pageSizeOffset:], pageSize)

	return page
}

func TestInspect_AcceptsWhatEtcdWrites(t *testing.T) {
	t.Parallel()

	// 4096 is what a snapshot from this cluster carries, measured.
	got, err := inspect(metaPage(boltMagic, boltVersion, 4096))

	require.NoError(t, err)
	assert.Equal(t, uint32(4096), got)
}

func TestInspect_AcceptsEveryPageSizeBoltWrites(t *testing.T) {
	t.Parallel()

	for size := range pageSizes {
		got, err := inspect(metaPage(boltMagic, boltVersion, size))

		require.NoError(t, err, size)
		assert.Equal(t, size, got)
	}
}

func TestInspect_Errors(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		page []byte
		want string
	}{
		// The case this command exists for: something that is not a snapshot
		// at all, named as one.
		"wrong magic": {metaPage(0xDEADBEEF, boltVersion, 4096), "not an etcd snapshot"},

		// A format this has never been tested against is not a thing to wipe
		// a control plane on the strength of.
		"wrong version": {metaPage(boltMagic, 99, 4096), "format version"},

		// A page size bbolt does not write means the header is being read as
		// something it is not, even if the magic happened to match.
		"impossible page size": {metaPage(boltMagic, boltVersion, 1234), "does not write"},

		"too short": {make([]byte, 8), "want at least"},
	} {
		_, err := inspect(tc.page)

		require.Error(t, err, name)
		assert.Contains(t, err.Error(), tc.want, name)
	}
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
