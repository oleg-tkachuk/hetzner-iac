# The domain

What being reachable from outside needs, and in what order. The fields
themselves are listed in [configuration.md](configuration.md#the-topology-file)
with everything else a topology holds; this is what they mean and what to do
about them.

Nothing here needs a domain to come up, and two things need one to be useful:
`40-ingress` creates the DNS records that point at the ingress load balancer,
and `30-cluster-services` orders the certificate for them. So a domain is a
prerequisite of being **reachable**, not of installing.

Leave `metadata.domain` out and both layers say so on every apply — no records
are written, `50-gitops` creates no Ingress, and the Argo CD UI is reached with
`kubectl port-forward`. The cluster is unaffected either way: `talosctl` and
`kubectl` target addresses, not names.

What the domain has to be:

- **one you control.** Let's Encrypt proves control of the name before it signs
  anything for it.
- **two labels or more.** A single label is a hostname, not a domain, and an
  order for one is refused — which without the check would surface after the
  Ingress was already in place. Validation refuses it at plan time instead.
- **registered anywhere, and its DNS hosted anywhere.** The registrar does not
  matter and neither does the TLD. Whether Hetzner serves the zone decides one
  thing only: who writes the two records, this repository or you.

## Two fields, because a zone cut cannot be derived from a name

| Field | Holds | Example |
|-------|-------|---------|
| `metadata.domain` | the name this environment is reached at | `platform.example.com` |
| `metadata.dnsZone` | the zone as delegated, when Hetzner holds its authoritative DNS | `example.com` |

`platform.example.com` could be a record called `platform` in the zone
`example.com`, or the apex of a zone `platform.example.com` delegated on its
own. Nothing in the name says which, so the zone is a second field rather than
a guess — and `domain` must equal it or sit under it, or the topology is
refused.

| `dnsZone` | `domain` | what `40-ingress` writes |
|-----------|----------|--------------------------|
| `example.com` | `platform.example.com` | a record `platform` in `example.com` |
| `example.com` | `example.com` | the zone apex |
| `dev.example.com` | `argocd.dev.example.com` | a record `argocd` in `dev.example.com` |
| empty | `platform.example.com` | nothing — the records are yours to write |

Both an `A` and an `AAAA` in every case, because a Hetzner load balancer has
both and an IPv4-only record fails for exactly the clients nobody tests from.

## The zone is looked up, never created

Create the zone in Hetzner DNS by hand, once, and point the registrar's `NS`
records at the nameservers that zone is served from. That delegation outlives
every cluster this repository builds, which is why no layer creates it: a zone
this stack owned would be a zone `pulumi destroy` deletes, with every record in
it — including the records for everything else that shares the domain.

`metadata.dnsZone` has to name a zone the Hetzner project actually holds. One
that is not there fails the apply and says what to do about it, rather than
creating anything:

```
the zone has to exist and be delegated to Hetzner: point the registrar's NS
records at Hetzner's nameservers, or leave metadata.dnsZone empty and manage
the records where the domain is hosted
```

## When the DNS is hosted somewhere else

That is a supported shape rather than a workaround, and for a domain already
serving something it is the normal one. Set `metadata.domain`, leave
`metadata.dnsZone` empty, and exactly two records become yours to write.
Nothing else changes: the load balancer, the Ingress and the certificate are
the same.

**The certificate does not care who serves the zone.** The ClusterIssuer solves
HTTP-01 through the ingress class `40-ingress` registers, so Let's Encrypt
validates by fetching `/.well-known/acme-challenge/` over the load balancer —
no provider credentials, no DNS-01, nothing a registrar has to support. What it
does need is for the name to resolve *to that balancer*, so the two records
come first and the order is the thing that waits: until they resolve, the
`Certificate` sits pending with an `Order` that keeps retrying, which looks
like nothing happening and is not an error.

Instead of writing those records, `40-ingress` says on every apply that they
are not its to write, and names the output holding the value they need:

```
metadata.dnsZone unset, so platform.example.com is hosted elsewhere — point it
at the ingressIp output by hand
```

```bash
task platform:outputs stack=dev layer=40-ingress
```

| Record, for `metadata.domain` | Value | Output |
|-------------------------------|-------|--------|
| `A` | the ingress load balancer's IPv4 | `ingressIp` |
| `AAAA` | its IPv6 | `ingressIpv6` |

Both of them. A missing `AAAA` fails for IPv6-only clients and for nobody
testing from a laptop.

Keep the TTL short. The records this repository writes for itself use 300
seconds, and the reason applies to yours identically: a load balancer's IPv4
cannot be reserved — it is neither a primary nor a floating IP — so a rebuilt
environment serves from a new address, and however long the old one stays
cached is how long the domain is dark.

**The whole cost of hosting the DNS elsewhere is that those two records are not
reconciled.** A rebuild gives a new address and nothing updates them or says
they are stale. Everything downstream of the name is unaffected.

## Delegating one subdomain, and leaving the rest where it is

The two are not exclusive, because delegation is per zone and not per domain.
Point `NS` records for one subdomain at Hetzner — `dev.example.com`, while
`example.com` stays with the provider it is on — and that subdomain is a zone
here like any other:

```yaml
metadata:
  name: platform-dev
  domain: platform.dev.example.com
  dnsZone: dev.example.com
```

`40-ingress` writes the records again, and the apex and everything else under
it is untouched. It is also the case that `dnsZone` exists for: the zone cut in
`platform.dev.example.com` is not its last two labels, and no rule could have
guessed it.

## The certificate, and the order to do it in

`cluster-services:acmeEmail` is what enables the ClusterIssuer at all — omit it
and none is created, so nothing is ordered. It is the one value here that is
genuinely layer-local rather than part of the cluster's shape:

```bash
pulumi -C layers/30-cluster-services config set --stack dev acmeEmail ops@example.com
```

`cluster-services:acmeStaging` picks the endpoint, and it defaults to
**production**. Set it `true` for a new domain's first attempt:

```bash
pulumi -C layers/30-cluster-services config set --stack dev acmeStaging true
```

Let's Encrypt's production endpoint rate-limits per registered domain, and a
first attempt is the likeliest one to fail — DNS not propagated yet, a record
pointing at the previous load balancer, a host Traefik has not admitted. The
staging endpoint issues from an untrusted root through the same flow, so the
browser warns and the certificate is still proof that the whole path works:
the name resolves, the balancer forwards, Traefik admits the host, the
challenge is answered. Then set it back to `false` and let the production order
replace it.

## One name, spelled once

Argo CD's hostname is `metadata.domain` itself, not a key of its own.
`40-ingress` points the records at its load balancer and `50-gitops` hands Argo
CD the same name, because two copies of one name drift — and an Ingress for one
name behind a record for another is accepted by everything and serves nothing.
