package main

import (
	"os"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/pkg/charts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAlloyImage_TracksThePinnedChart(t *testing.T) {
	t.Parallel()

	// The version that matters is the one the cluster will run. A hard-coded
	// tag would keep validating an Alloy nobody deploys the moment the chart
	// is bumped.
	image, err := alloyImage()
	require.NoError(t, err)

	chart := charts.MustGet("alloy")
	require.NotEmpty(t, chart.AppVersion, "the alloy entry must carry an app version")

	assert.Equal(t, "grafana/alloy:"+chart.AppVersion, image)
}

func TestIndent(t *testing.T) {
	t.Parallel()

	// Alloy's error output is multi-line; indenting keeps the tool's own
	// message distinguishable from what Alloy said.
	assert.Equal(t, "      one\n      two", indent("one\ntwo\n"))
}

func TestWriteConfig(t *testing.T) {
	t.Parallel()

	// The file must hold the configuration the layer deploys, and be readable
	// by the container user — the image does not run as root.
	path, err := writeConfig()
	require.NoError(t, err)
	require.NotEmpty(t, path)

	t.Cleanup(func() { _ = os.Remove(path) })

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(content), "loki.write")

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode().Perm()&0o044, "the container user must be able to read it")
}
