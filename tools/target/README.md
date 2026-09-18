# target

Resolves a component name into the URNs `pulumi --target` takes.

```bash
go run ./tools/target layers/30-cluster-services dev cert-manager
urn:pulumi:dev::cluster-services::kubernetes:helm.sh/v3:Release::cert-manager
```

Used by `task platform:plan|apply|destroy … target=<selector>`. Not usually
run by hand.

## Why it exists

Because a `--target` that matches nothing **succeeds**. Measured against this
repository's own dev stack:

```
$ pulumi preview --target '**::Release::does-not-exist'
Resources:
    + 1 to create
    24 unchanged        ← exit 0
```

So `task platform:apply … target=cert-manger` would report success and change
nothing. That is the same shape of failure `tasks/platform.task.yaml` already
records about a `for` loop over an empty list: *"would have reported success and
applied no layer at all"*. Pulumi will not catch it, so this does.

The `+ 1 to create` there is a provider. Providers are created regardless of
`--target`, which is worth knowing before reading it as a bug.

## Selectors

| Form | Selects |
|------|---------|
| `cert-manager` | the resource with that name, whatever its type |
| `ConfigFile:kubelet-serving-cert-approver` | the same, qualified, when one name is used by two types |
| `group:Ingress` | a group's own node and every resource under it |
| `hcloud-ccm,hcloud-csi` | both, as one `pulumi` run with two `--target` flags |

A comma-separated list resolves every selector, and **every one of them has to
resolve**: a list where one name is a typo refuses as a whole rather than
applying the rest, because a partial apply that reports success is the failure
this program exists to prevent. An empty element — `a,` from an unset shell
variable — is refused for the same reason. Selectors may overlap, so
`group:Ingress,traefik` is allowed and yields each URN once.

A comma because it cannot appear in a resource name, a type token or a URN, so
splitting on it can never cut a selector in half.

`group:` reads what the state already says. A component resource's type appears
in the URN of everything beneath it:

```
urn:…::hetzner-iac:platform:Ingress$kubernetes:helm.sh/v3:Release::traefik
```

so the group's node is found first and its **full** type token is what children
are matched by. Matching on the type's last segment alone was tried and was
wrong: a Kubernetes `Ingress` object has the same leaf, and `group:Ingress`
then meant every Ingress in the cluster. A group's type must begin with
`hetzner-iac:`, which a provider's type never does.

## Three answers, and the middle one is the point

- **Resolved** — the URNs, one per line, in state order.
- **Nothing matched** — exit 1, and the list of what the stack does hold,
  written as `Type:name` so it doubles as the syntax for the qualified form.
  Nothing is filtered out of that list: hiding the stack's own node and its
  providers would be tidier and would also be a judgement about what an
  operator is allowed to look for.
- **One name, two types** — exit 1, and every qualified form. Not a guess at
  the intended one: suggesting the alphabetically first type would be arbitrary
  advice with the authority of a tool behind it.

## Where the resources come from

`pulumi stack --show-urns --output json`, which returns exactly `{urn, type,
name}` per resource. Not `stack export`, which returns the whole checkpoint —
every resource's inputs, including the ciphertext of the Hetzner token — and is
more than the question asks for.

Not the display output of `pulumi stack --show-urns` either: those columns are
laid out for a terminal and move between releases.

There is nothing here for a list to drift from. The question "does this name
exist" is asked of the only thing that knows.
