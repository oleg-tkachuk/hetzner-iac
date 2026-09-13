# Security policy

## Reporting a vulnerability

Report privately through GitHub's **[Report a
vulnerability](https://github.com/oleg-tkachuk/hetzner-iac/security/advisories/new)**
form. It opens a draft advisory visible only to you and the maintainer.

Please do not open a public issue for anything exploitable.

Include what you would need yourself: the affected file or task, what an
attacker gains, and the shortest way to observe it. A proof of concept helps
more than a severity score.

This is a solo-maintained repository, so the honest expectation is a first
reply within a week. If a report goes unanswered for longer, open a public
issue saying only that a private report is waiting — no detail.

## What is in scope

This repository is infrastructure code. The interesting failures are in what it
builds, not in a running service:

- a default that exposes a cluster — an open firewall rule, a permissive Pod
  Security setting, a credential reachable by a workload that should not have
  it;
- a supply-chain weakness in how versions are pinned or verified: a chart, an
  image, a GitHub Action or a Go module that could be substituted;
- a workflow that could leak a secret — in particular anything that would let
  a pull request from a fork reach one;
- a credential committed to the repository or recoverable from its history.

Out of scope: vulnerabilities in Hetzner Cloud, Talos Linux, Kubernetes or the
upstream charts themselves. Report those to their own projects. If a pinned
version here carries a known vulnerability, that *is* in scope — say which pin.

## What is already assumed public

Two things look like findings and are not, so a report about them will be
closed:

**The `secure:` ciphertext in `Pulumi.<stack>.yaml`.** Stack secrets are
encrypted by the Pulumi Cloud backend, which holds the key; the repository does
not. Those files are gitignored here regardless, because they carry one
operator's environment. On a self-managed backend the same field is encrypted
with `PULUMI_CONFIG_PASSPHRASE` instead — that ciphertext must never reach a
public repository at all.

**The perimeter design.** The firewall opens the Kubernetes and Talos APIs to
`network.adminCIDRs` and nothing else, and the topology file naming those
addresses is deliberately not committed. Describing the design is not a
weakness in it.

## Supported versions

The latest release, from `main`. There are no maintenance branches.
