package hetzner

import (
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

// toStringMap converts a plain map into Pulumi inputs. A nil map becomes an
// empty StringMap rather than nil: hcloud treats a missing labels field and
// an empty one differently on update, and the empty form is what makes
// removing the last label actually remove it.
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
