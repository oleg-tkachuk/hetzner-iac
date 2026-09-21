package layer_test

import (
	"encoding/json"
	"testing"

	"github.com/oleg-tkachuk/hetzner-iac/internal/pkg/layer"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// theSwitch stands for whatever flag a layer is reading, so the assertions can
// say "the message names the key" without a literal that means nothing.
const theSwitch = "someSwitch"

// setSwitch points the layer at a cluster stack and sets one switch beside it.
//
// Through PULUMI_CONFIG rather than a fake config object, which is the whole
// point of the test: it is the SDK's own accessor that has to refuse the
// value, and a fake would only prove this file agrees with itself. t.Setenv is
// why these do not run in parallel.
func setSwitch(t *testing.T, value string, set bool) {
	t.Helper()

	config := map[string]string{testProject + ":clusterStackRef": "acme/hetzner-cluster/prod"}
	if set {
		config[testProject+":"+theSwitch] = value
	}

	raw, err := json.Marshal(config)
	require.NoError(t, err)

	t.Setenv("PULUMI_CONFIG", string(raw))
}

// TestFlag_RefusesAValueThatIsNotABoolean is the behaviour the accessor exists
// with rather than without, and it was two hand-written copies of this table
// in two layers before.
//
// The refused spellings are the ones that cost something: `yes` and `on` are
// YAML's own word for true and a shell's, and the cast behind Pulumi's config
// takes neither — so read through GetBool they mean false, which is
// indistinguishable from never having asked.
func TestFlag_RefusesAValueThatIsNotABoolean(t *testing.T) {
	for name, one := range map[string]struct {
		value   string
		unset   bool
		want    bool
		wantErr bool
	}{
		"unset is off":                      {unset: true, want: false},
		"true":                              {value: "true", want: true},
		"false":                             {value: "false", want: false},
		"True, because YAML has":            {value: "True", want: true},
		"1 is what the cast takes":          {value: "1", want: true},
		"0":                                 {value: "0", want: false},
		"an empty value is off":             {value: "", want: false},
		"yes is refused, not read as false": {value: "yes", wantErr: true},
		"on is refused too":                 {value: "on", wantErr: true},
		"y is refused":                      {value: "y", wantErr: true},
		"a typo is refused":                 {value: "ture", wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			setSwitch(t, one.value, !one.unset)

			var (
				got     bool
				flagErr error
			)

			require.NoError(t, run(t, newMocks(), func(runner *layer.Runner) error {
				got, flagErr = runner.Flag(theSwitch)

				return nil
			}))

			if one.wantErr {
				require.Error(t, flagErr, "%q was accepted", one.value)
				assert.False(t, got, "a refused value must not also report true")
				assert.Contains(t, flagErr.Error(), theSwitch,
					"the failure does not name the key the operator has to fix")
				assert.Contains(t, flagErr.Error(), one.value,
					"the failure does not quote what was actually set")

				return
			}

			require.NoError(t, flagErr, "%q", one.value)
			assert.Equal(t, one.want, got, "%q", one.value)
		})
	}
}
