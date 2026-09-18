package layer

import (
	"encoding/base64"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumix"
)

// Base64Of encodes a value for a Secret's `data` field. Secretness survives
// the apply, so a token stays marked as one.
//
// Here rather than in each layer that writes a Secret, because it was in two
// of them, character for character, with the same unchecked type assertion in
// each. `dupl` never saw it: the threshold is 200 lines, set for chart value
// maps that repeat by nature.
//
// `data` and not `stringData`, which is the reason this exists at all.
// stringData is write-only — Kubernetes folds it into data, and the provider
// records data in the state — so a program setting stringData diffs against
// its own last apply and plans to replace the Secret on every run, for ever.
func Base64Of(value pulumi.StringInput) pulumi.StringOutput {
	return pulumix.Cast[pulumi.StringOutput](pulumix.Apply(
		value.ToStringOutput(),
		func(raw string) string {
			return base64.StdEncoding.EncodeToString([]byte(raw))
		},
	))
}
