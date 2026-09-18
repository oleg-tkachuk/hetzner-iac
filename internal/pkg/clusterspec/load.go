package clusterspec

import (
	"fmt"
	"os"

	"sigs.k8s.io/yaml"
)

// LoadTopology reads, defaults and validates a committed topology file.
//
// Defaulting happens before validation so that a sparse file is judged on the
// values that will actually be applied, not on the blanks it left.
func LoadTopology(path string) (*Topology, error) {
	// #nosec G304 -- the path names a committed topology file chosen by the
	// operator; reading the file they asked for is what this function is for.
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read cluster topology: %w", err)
	}

	return ParseTopology(raw)
}

// ParseTopology is LoadTopology without the filesystem, so callers that
// already hold the bytes — and tests — do not have to write a file.
func ParseTopology(raw []byte) (*Topology, error) {
	var topology Topology

	// UnmarshalStrict rather than Unmarshal: a misspelled key in a file that
	// describes a cluster should stop the run, not silently keep a default.
	// "workerpools" instead of "workerPools" would otherwise build a cluster
	// with no workers and report success.
	if err := yaml.UnmarshalStrict(raw, &topology); err != nil {
		return nil, fmt.Errorf("parse cluster topology: %w", err)
	}

	topology.ApplyDefaults()

	if err := topology.Validate(); err != nil {
		return nil, err
	}

	return &topology, nil
}
