package platform_test

import (
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/platform"
	"github.com/stretchr/testify/assert"
)

func TestStorageClass_IsWhatTheCSIDriverRegisters(t *testing.T) {
	t.Parallel()

	// The hcloud CSI driver registers this name, and it is not configurable
	// from the chart values this repository sets. Pinned because the failure
	// is silent: a claim for a class that does not exist stays Pending, and so
	// does everything waiting on the volume.
	assert.Equal(t, "hcloud-volumes", platform.StorageClass)
}
