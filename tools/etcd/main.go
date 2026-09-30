// Command etcd answers questions about an etcd snapshot file.
//
// It exists for one moment: `task cluster:etcd:restore` wipes the EPHEMERAL
// partition of every control-plane node before it can restore anything, and
// doing that on the strength of a truncated download is how a recoverable
// incident becomes an unrecoverable one. So the file is checked before the
// cluster is touched, not after.
//
// The check is bbolt's own, not a reimplementation of it. A snapshot is a
// bbolt database, and opening one validates the magic, the format version,
// the page size and the meta page checksum — all of which this used to read
// by hand from copied offsets, correct for exactly the file it was written
// against.
//
// Not go.etcd.io/etcd/etcdutl/v3, which has `snapshot status` as a library
// and would be the more obvious choice: it brings the etcd server and raft
// with it, thirteen modules to read a header. bbolt is the library that
// defines the format, and it is one.
package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/clusterspec"
)

// Buckets every etcd snapshot carries. A bbolt database without them opens
// perfectly and is not an etcd snapshot — someone else's database, or the
// wrong file with the right extension.
//
// `key` holds the keyspace, `meta` the consistency bookkeeping, `members` and
// `cluster` the membership a recovery rebuilds from.
var requiredBuckets = []string{keyBucket, metaBucket, membersBucket, clusterBucket}

const (
	// metaBucket and consistentIndexKey are where etcd records how far raft
	// had applied when the snapshot was taken.
	//
	// NOT the revision `talosctl etcd snapshot` prints, which is the MVCC
	// revision: measured on one snapshot, revision 39170 against consistent
	// index 6028. Two counters, and reporting either as the other would have
	// somebody comparing the wrong numbers during a restore.
	//
	// It restarts after a recovery bootstrap, because that begins a new raft
	// cluster — so it separates two snapshots of one cluster, and says
	// nothing across a restore.
	metaBucket         = "meta"
	consistentIndexKey = "consistent_index"

	// membersBucket holds one entry per etcd member, keyed by member ID. The
	// value is the member as JSON, and its name is the Talos node name — which
	// carries the name of the cluster that took the snapshot.
	membersBucket = "members"

	// clusterBucket holds the cluster-wide settings a recovery rebuilds from.
	clusterBucket = "cluster"

	// keyBucket holds one entry per REVISION, not per key — etcd is
	// multi-version, so this is larger than the key count etcdutl reports and
	// must not be labelled as keys.
	keyBucket = "key"

	// openTimeout bounds the flock bbolt takes. A snapshot file nothing else
	// is using opens at once; a hang here would be a lock held by something
	// that should not have it, and waiting forever hides that.
	openTimeout = 5 * time.Second

	// fileMode is read-only: this command never writes to a snapshot.
	fileMode = 0o400
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// Usage is what a wrong argument list gets.
const Usage = "usage: etcd verify --cluster <cluster name> <snapshot>"

var errUsage = errors.New(Usage)

func run(args []string) error {
	if len(args) == 0 || args[0] != "verify" {
		return errUsage
	}

	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	cluster := flags.String("cluster", "", "the cluster the snapshot must have been taken from")

	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 1 || *cluster == "" {
		return errUsage
	}

	path := flags.Arg(0)

	// #nosec G703 -- the path names the snapshot the operator asked to
	// restore from, and reading it is the whole command. The taint analysis
	// cannot see that an operator naming their own backup file is the input,
	// not an attacker's.
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	// ReadOnly, so a corrupt file is never written back to, and bbolt's own
	// validation is what rejects a truncated or foreign one.
	db, err := bolt.Open(path, fileMode, &bolt.Options{ReadOnly: true, Timeout: openTimeout})
	if err != nil {
		return fmt.Errorf("%s is not a readable bbolt database, so it is not an etcd snapshot: %w", path, err)
	}
	// Opened read-only, so a Close error cannot mean data was lost — and the
	// verdict this function returns is about the file's contents, which have
	// already been read by the time this runs.
	defer func() { _ = db.Close() }()

	facts, err := inspect(db)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	// Before anything is reported as good. Every snapshot is a valid etcd
	// database; what makes one safe to restore is that it came from THIS
	// cluster, and nothing else about the file says so — the file name did
	// not carry the stack, and upload picked the newest of any.
	if err := belongsTo(facts.Members, *cluster); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	fmt.Printf("%s — %d bytes, %d revisions, consistent index %d, %d members of %s\n",
		path, info.Size(), facts.Revisions, facts.ConsistentIndex, len(facts.Members), *cluster)

	return nil
}

// Facts are what a snapshot says about itself.
type Facts struct {
	// Revisions is the number of entries in the key bucket, which is one per
	// revision rather than one per key: 3278 entries against the 1616 keys
	// talosctl counted in the same snapshot.
	Revisions int

	// ConsistentIndex is how far raft had applied when the snapshot was
	// taken. Zero is a valid value only for a cluster that has applied
	// nothing, which a real one never is.
	ConsistentIndex uint64

	// Members are the names of the etcd members, which are the names of the
	// control-plane nodes. A member added but never started has no name yet
	// and is not listed.
	Members []string
}

// consistencyProblems drains bbolt's check of every page reachable in tx.
// The channel has to be read to the end: the check runs in a goroutine that
// closes it when done.
func consistencyProblems(tx *bolt.Tx) []error {
	var problems []error

	for problem := range tx.Check() {
		problems = append(problems, problem)
	}

	return problems
}

// member is the part of etcd's stored member this reads. etcd's own type is
// in its server module, which this tool declines for the reason given at the
// top of the file; the field is the one `etcdctl member list` prints as NAME.
type member struct {
	Name string `json:"name"`
}

// belongsTo refuses a snapshot any of whose members is not a control-plane
// node of cluster.
//
// Every member rather than any: a snapshot is one etcd cluster, so one
// foreign name already means the file is not this cluster's.
func belongsTo(members []string, cluster string) error {
	if len(members) == 0 {
		return errors.New("records no named etcd member, so nothing says which cluster it came from")
	}

	prefix := clusterspec.NodeNamePrefix(cluster, clusterspec.RoleControlPlane)

	var foreign []string

	for _, name := range members {
		ordinal, found := strings.CutPrefix(name, prefix)
		if _, err := strconv.Atoi(ordinal); !found || err != nil {
			foreign = append(foreign, name)
		}
	}

	if len(foreign) > 0 {
		return fmt.Errorf("taken from another cluster: member(s) %s are not control-plane nodes of %s",
			strings.Join(foreign, ", "), cluster)
	}

	return nil
}

// inspect reads what a snapshot says about itself, and refuses a database
// that is not one.
//
// Takes the open database rather than a path so a test can build a fixture
// with bbolt itself — the same library production files are read with, which
// a struct of crafted bytes was not.
func inspect(db *bolt.DB) (Facts, error) {
	var facts Facts

	err := db.View(func(tx *bolt.Tx) error {
		// Every page first, with bbolt's own consistency check, and before
		// anything below reads one. Opening validated the meta pages and
		// nothing else: a damaged data page opened fine and then crashed the
		// first read of it, and in a bucket this does not read it was never
		// looked at — and the restore wipes the nodes on this verdict.
		if problems := consistencyProblems(tx); len(problems) > 0 {
			return fmt.Errorf("fails bbolt's consistency check (%d problem(s)): %w",
				len(problems), problems[0])
		}

		for _, name := range requiredBuckets {
			if tx.Bucket([]byte(name)) == nil {
				return fmt.Errorf("a bbolt database with no %q bucket: not an etcd snapshot", name)
			}
		}

		facts.Revisions = tx.Bucket([]byte(keyBucket)).Stats().KeyN

		// Stored big-endian by etcd. A short value means the bucket exists
		// and holds something else, which is worth refusing rather than
		// reading as a small number.
		raw := tx.Bucket([]byte(metaBucket)).Get([]byte(consistentIndexKey))
		if len(raw) != 8 {
			return fmt.Errorf("%s/%s is %d bytes, want 8", metaBucket, consistentIndexKey, len(raw))
		}

		facts.ConsistentIndex = binary.BigEndian.Uint64(raw)

		return tx.Bucket([]byte(membersBucket)).ForEach(func(_, value []byte) error {
			var stored member
			if err := json.Unmarshal(value, &stored); err != nil {
				return fmt.Errorf("%s holds an entry that is not an etcd member: %w", membersBucket, err)
			}

			if stored.Name != "" {
				facts.Members = append(facts.Members, stored.Name)
			}

			return nil
		})
	})

	return facts, err
}
