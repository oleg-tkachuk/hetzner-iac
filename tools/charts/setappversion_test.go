package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/charts"
)

const declaration = `package charts

func init() {
	register(Definition{
		Key: ArgoCD,
		Chart: Chart{
			Name:       "argo-cd",
			Repo:       "https://argoproj.github.io/argo-helm",
			Version:    "10.10.3", // app v3.5.4
			AppVersion: "v3.5.4",
			Namespace:  "argocd",
		},
	})
}
`

var argoCD = charts.Chart{Name: "argo-cd", Repo: "https://argoproj.github.io/argo-helm", Version: "10.10.3"}

func TestSetAppVersion_ChangesOnlyTheFieldAndItsComment(t *testing.T) {
	t.Parallel()

	out, err := setAppVersion([]byte(declaration), argoCD, "v3.5.5")
	require.NoError(t, err)

	want := strings.ReplaceAll(declaration, "v3.5.4", "v3.5.5")
	assert.Equal(t, want, string(out))
}

func TestSetAppVersion_WritesACommentThatWasMissing(t *testing.T) {
	t.Parallel()

	src := strings.Replace(declaration, ` // app v3.5.4`, "", 1)

	out, err := setAppVersion([]byte(src), argoCD, "v3.5.5")
	require.NoError(t, err)
	assert.Contains(t, string(out), `Version:    "10.10.3", // app v3.5.5`+"\n")
	assert.Contains(t, string(out), `AppVersion: "v3.5.5",`)
}

func TestSetAppVersion_Refuses(t *testing.T) {
	t.Parallel()

	_, err := setAppVersion([]byte(declaration), argoCD, "")
	require.Error(t, err, "upstream publishing none means removing the field, which is a person's call")

	noField := strings.Replace(declaration, "\t\t\tAppVersion: \"v3.5.4\",\n", "", 1)
	_, err = setAppVersion([]byte(noField), argoCD, "v3.5.5")
	require.ErrorIs(t, err, errNoAppVersionField, "adding the field is a person's call too")

	stale := argoCD
	stale.Version = "10.10.2"
	_, err = setAppVersion([]byte(declaration), stale, "v3.5.5")
	require.ErrorIs(t, err, errNoAppVersionField, "a pin the file does not declare")
}

// TestSetAppVersion_MatchesEveryRegisteredChart runs against the real
// declarations, so a reformatted file fails here rather than inside Renovate.
func TestSetAppVersion_MatchesEveryRegisteredChart(t *testing.T) {
	t.Parallel()

	for _, key := range charts.Keys() {
		chart := charts.MustGet(key)
		if chart.AppVersion == "" {
			continue
		}

		src, err := os.ReadFile(filepath.Join("..", "..", chartsDir, key+".go"))
		require.NoError(t, err, key)

		out, err := setAppVersion(src, chart, chart.AppVersion)
		require.NoError(t, err, key)

		assert.Equal(t, string(src), string(out), "%s: rewriting to the current value changes nothing", key)
	}
}

func TestWriteAppVersionIn(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, charts.ArgoCD+".go")
	require.NoError(t, os.WriteFile(path, []byte(declaration), chartSourceMode))

	require.NoError(t, writeAppVersionIn(dir, charts.ArgoCD, argoCD, "v3.5.5"))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, strings.ReplaceAll(declaration, "v3.5.4", "v3.5.5"), string(raw))

	err = writeAppVersionIn(dir, charts.ArgoCD, argoCD, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), path+": ", "a refusal names the file")

	require.ErrorIs(t, writeAppVersionIn(dir, "absent", argoCD, "v1"), os.ErrNotExist)
}
