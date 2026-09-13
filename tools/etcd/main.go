// Command etcd answers questions about an etcd snapshot file.
//
// It exists for one moment: `task cluster:etcd-restore` wipes the EPHEMERAL
// partition of every control-plane node before it can restore anything, and
// doing that on the strength of a truncated download is how a recoverable
// incident becomes an unrecoverable one. So the file is checked before the
// cluster is touched, not after.
//
// A snapshot is a bbolt database — the same format etcd stores its keyspace
// in — so the check is a real one rather than a size threshold: the meta page
// carries a magic number, a format version, and the page size that says where
// the second meta page must be.
package main

import (
	"encoding/binary"
	"fmt"
	"os"
)

// The bbolt meta page, measured against a snapshot this repository took.
//
// Offsets are into the first page; the second meta page repeats at pageSize
// plus the same offset, which is what makes the page size self-checking.
const (
	boltMagic   = 0xED0CDAED
	boltVersion = 2

	magicOffset    = 16
	versionOffset  = 20
	pageSizeOffset = 24

	// headerBytes is enough for the first meta page at any page size bbolt
	// uses, so one read answers everything about the first page.
	headerBytes = 64
)

// Page sizes bbolt is willing to write. A snapshot claiming anything else is
// either corrupt or not a snapshot, and either way is not something to wipe a
// control plane for.
var pageSizes = map[uint32]bool{4096: true, 8192: true, 16384: true, 65536: true}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 2 || args[0] != "verify" {
		return fmt.Errorf("usage: etcd verify <snapshot>")
	}

	path := args[1]

	// #nosec G304,G703 -- the path names the snapshot the operator asked to
	// restore from, and reading it is the whole command. The taint analysis
	// cannot see that an operator naming their own backup file is the input,
	// not an attacker's.
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	info, statErr := file.Stat()
	if statErr != nil {
		return fmt.Errorf("stat %s: %w", path, statErr)
	}

	header := make([]byte, headerBytes)
	if _, readErr := file.ReadAt(header, 0); readErr != nil {
		return fmt.Errorf("%s is %d bytes, too short to be an etcd snapshot: %w",
			path, info.Size(), readErr)
	}

	pageSize, inspectErr := inspect(header)
	if inspectErr != nil {
		return fmt.Errorf("%s: %w", path, inspectErr)
	}

	// The second meta page, at the page size the first one claims. A file
	// that has the right first page and nothing after it is a truncated
	// download, which is the case this whole command exists for.
	second := make([]byte, headerBytes)
	if _, readErr := file.ReadAt(second, int64(pageSize)); readErr != nil {
		return fmt.Errorf("%s has one meta page and no second at offset %d: truncated: %w",
			path, pageSize, readErr)
	}

	if _, inspectErr := inspect(second); inspectErr != nil {
		return fmt.Errorf("%s: second meta page at offset %d: %w", path, pageSize, inspectErr)
	}

	fmt.Printf("%s — bbolt v%d, %d byte pages, %d bytes\n", path, boltVersion, pageSize, info.Size())

	return nil
}

// inspect reads one bbolt meta page and returns the page size it declares.
//
// Separated from the file handling so it can be tested on crafted bytes: the
// cases worth covering are a wrong magic, a wrong version and an impossible
// page size, and writing three corrupt snapshots to disk to cover them would
// test os.ReadAt rather than this.
func inspect(page []byte) (uint32, error) {
	if len(page) < headerBytes {
		return 0, fmt.Errorf("meta page is %d bytes, want at least %d", len(page), headerBytes)
	}

	if magic := binary.LittleEndian.Uint32(page[magicOffset:]); magic != boltMagic {
		return 0, fmt.Errorf("magic is %#x, want %#x: not an etcd snapshot", magic, boltMagic)
	}

	if version := binary.LittleEndian.Uint32(page[versionOffset:]); version != boltVersion {
		return 0, fmt.Errorf("bbolt format version is %d, want %d", version, boltVersion)
	}

	pageSize := binary.LittleEndian.Uint32(page[pageSizeOffset:])
	if !pageSizes[pageSize] {
		return 0, fmt.Errorf("declares a %d byte page size, which bbolt does not write", pageSize)
	}

	return pageSize, nil
}
