package hetzner

import (
	"fmt"
	"strconv"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumix"
)

// idToInt converts a resource ID — which Pulumi models as an opaque string —
// into the numeric form hcloud's own arguments expect.
//
// hcloud IDs are always numeric, so a parse failure here is not a malformed
// cloud response but a programming error: the ID of something that is not an
// hcloud resource. It surfaces as a failed output rather than a panic so the
// run reports which resource, instead of a stack trace.
func idToInt(id pulumi.IDOutput) pulumi.IntOutput {
	return pulumix.Cast[pulumi.IntOutput](pulumix.ApplyErr(id, func(raw pulumi.ID) (int, error) {
		return strconv.Atoi(string(raw))
	}))
}

// asSecret marks a credential as secret, and says so if Pulumi hands back
// something other than the string output it was given.
//
// pulumi.ToSecret takes and returns `any`, so its result has to be converted
// back. It was converted with an unchecked assertion in both places that call
// it — correct today, and a panic in the middle of an apply on the day the SDK
// returns a wrapper instead.
func asSecret(name string, value pulumi.StringOutput) (pulumi.StringOutput, error) {
	secret, ok := pulumi.ToSecret(value).(pulumi.StringOutput)
	if !ok {
		return pulumi.StringOutput{}, fmt.Errorf(
			"mark %s as secret: pulumi.ToSecret returned %T rather than a string output",
			name, pulumi.ToSecret(value))
	}

	return secret, nil
}

// toStringMap converts a plain map into Pulumi inputs. A nil map becomes an
// empty StringMap rather than nil: hcloud treats a missing labels field and an
// empty one differently on update, and the empty form is what makes removing
// the last label actually remove it.
func toStringMap(in map[string]string) pulumi.StringMapInput {
	out := make(pulumi.StringMap, len(in))
	for key, value := range in {
		out[key] = pulumi.String(value)
	}

	return out
}

// toStringArray converts a plain slice into Pulumi inputs.
func toStringArray(in []string) pulumi.StringArrayInput {
	out := make(pulumi.StringArray, 0, len(in))
	for _, value := range in {
		out = append(out, pulumi.String(value))
	}

	return out
}
