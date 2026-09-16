# ADR-0003: State backend and secrets management

**Status:** Accepted
**Date:** 2026-09-09
**Deciders:** Oleg Tkachuk

## Context

[ADR-0001](0001-provisioning-tool.md) settles Pulumi as the provisioning tool,
so state is Pulumi state and it is critical infrastructure: lose it and Pulumi
no longer knows what it owns; corrupt it with a concurrent write and it can
propose destroying live servers.

Pulumi encrypts secret values in state with a configured encryption provider,
and cloud credentials never enter state at all — they stay with the CLI. What
is unencrypted is every non-secret attribute, which still describes the estate
in detail.

## Decision

**State lives in Pulumi Cloud**, declared as `backend:` in every `Pulumi.yaml`
rather than left to whatever `pulumi login` last pointed at. The backend is
then a property of the repository, and a clone cannot apply against the wrong
one by inheriting a login. Pulumi Cloud coordinates concurrent updates, keeps
update history and manages the encryption key; the free individual tier covers
an estate this size.

Keeping state in an S3-compatible bucket instead is supported and is a URL
change:
[configuration.md](../configuration.md#keeping-state-in-your-own-s3-bucket) has
the form, and the one rule that changes with it — a DIY backend encrypts stack
secrets with a passphrase, which makes the ciphertext in `Pulumi.<stack>.yaml`
offline-attackable, so those files are not committed.

**Secrets Pulumi holds are encrypted by Pulumi.** Stack configuration set with
`pulumi config set --secret` — the Hetzner token above all, written by
`task cluster:token` from a prompt or from standard input so it never reaches
the shell history — is stored as `secure:` ciphertext.

**SOPS with age is the answer for secrets Pulumi does not hold**: material that
has to exist in the repository for something other than Pulumi to read. There
is none today, and no `.sops.yaml` exists. The choice is recorded so the
question is not reopened when the first one appears.

## Consequences

- State is durable, shared and versioned, and CI can apply without an
  interactive step.
- The Pulumi Cloud account becomes the highest-value credential in the system:
  it holds the key that decrypts every stack secret.
- `Pulumi.<stack>.yaml` is gitignored — not for the ciphertext, which is safe
  to publish while Pulumi Cloud holds the key, but because the file is one
  operator's environment: their token, their organisation, the stack their
  layers point at.
- Secret rotation is manual, and therefore easy to skip.

## To revisit

- A secrets manager with rotation and an audit log, once there are enough
  secrets or people that manual rotation stops happening.
- Off-vendor state replication, if a Hetzner-wide outage during an incident
  becomes a scenario worth engineering against — which only applies if state
  moves to a bucket at Hetzner.
