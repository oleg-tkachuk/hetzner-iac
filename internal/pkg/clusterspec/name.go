package clusterspec

// Name is this repository's own name, and the single place it is written.
//
// Five contracts are built from it, and the reason they share one constant is
// that each pair of them must agree exactly while nothing checks that at run
// time:
//
//   - the Pulumi type tokens of every component resource,
//     `<Name>:cluster:ControlPlane` and its four siblings, which appear in the
//     URN of every resource beneath them;
//   - the `group:` selector in tools/target, which must recognise those tokens
//     to tell a component of ours from a provider's resource of the same name;
//   - the `managed-by` label on every Hetzner resource this repository
//     creates, which is how `task cluster:orphans` tells them from anything
//     else in the project;
//   - the `apiVersion` of a topology document, which decides whether a
//     committed cluster file validates at all;
//   - the CrossGuard pack's name.
//
// Renaming it is not one change, and the four costs are not the same:
//
//   - the type tokens are STATE. A rename orphans every resource under a
//     renamed component — Pulumi sees the old ones gone and new ones arrived —
//     so it needs pulumi.Aliases, not a find-and-replace.
//   - the label is a SELECTOR. Renaming it makes every existing resource
//     invisible to the orphan check, which then reports a clean project while
//     the old resources keep billing.
//   - the apiVersion is a DOCUMENT contract. Renaming it fails every committed
//     topology at validation, which is loud and cheap.
//   - the pack name and the `group:` prefix are cosmetic by comparison.
//
// That spread is the argument for one constant: whoever renames this should
// meet all five in one place rather than find the fifth from an incident.
//
// Not derived from the Go module path on purpose. `go.mod` says
// github.com/oleg-tkachuk/hetzner-iac, and taking the last element of it would
// tie the state contract above to where the repository is hosted — moving the
// repository would rewrite every URN.
const Name = "hetzner-iac"
