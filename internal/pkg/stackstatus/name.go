package stackstatus

import "strings"

// orgSeparator separates an organisation from a stack in the fully qualified
// name Pulumi Cloud lists: `acme/dev`.
const orgSeparator = "/"

// SameStack reports whether a stack name as Pulumi lists it is the stack an
// operator asked for by its short name.
//
// Pulumi Cloud lists a stack as `org/dev` while every command here is given
// `dev`, and Pulumi resolves the short name against the current organisation
// on its own. An exact comparison therefore finds nothing on the backend the
// stacks actually live on, and a tool that reads "not found" as "never
// applied" then reports a working stack as absent. One rule, in one place,
// for every tool that looks a stack up in a listing.
func SameStack(listed, stack string) bool {
	return listed == stack || strings.HasSuffix(listed, orgSeparator+stack)
}
