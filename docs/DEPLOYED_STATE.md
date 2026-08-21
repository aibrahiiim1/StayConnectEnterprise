# Deployed state — activation, licensing, Central FQDN, Kea health

Current as of **2026-08-21**. Status: **PRE-LIVE**. No Go-Live, no guest traffic, no LAN/guest VLAN/DHCP
configuration, no PMS or payment traffic.

## What is deployed

Both hosts carry the head of `post-roadmap/activation-fqdn-kea-deploy` (PR #23). The live value is in
`/opt/stayconnect/DEPLOYED_SHA` on each host — that file, not this sentence, is the authority, because a
document cannot state the hash of the commit that contains it.

Two milestones worth naming:

- **`253674c2be15cb11d0f746baf73e2c07f1a4d4f2`** — the activation / licensing / FQDN / Kea deployment,
  verified end to end.
- **`b448abe`** — the last commit that changes runtime behaviour. Everything after it is deployment tooling
  (the DNS-gated cutover script, the corrected DNS check, Central's firewall rule) and this document.

Binaries are stamped with the commit they were **built from**. Where that is older than the head, the source
for that component is byte-identical between the two — checked with `git diff` over the component's paths —
so the artifact is current. Naming the build commit rather than the head keeps that verifiable instead of
assumed.

| | Host | Built from | Still current? |
|---|---|---|---|
| Central source + tooling | `sc-central` (150.0.0.252) | branch head | — |
| ctrlapi binary | same | `5477a2b` | yes — `control-plane/` unchanged since |
| cloud-admin bundle | same | `5477a2b` | yes — `cloud-admin/` unchanged since |
| Appliance source + tooling | `sce` (172.21.60.25) | branch head | — |
| scd | same | `4be7adf` | yes — `data-plane/` unchanged since |
| edged, netd | same | `97a9b9d` | yes — their `cmd/` trees unchanged since |
| hotel-admin bundle | same | `5477a2b` | yes — `hotel-admin/` unchanged since |

Branch `post-roadmap/activation-fqdn-kea-deploy`, PR #23, **unmerged** by instruction.

## Central endpoint

`https://sc-central.echofusion.com`, defined once in
[`deploy/config/central-endpoint.env`](../deploy/config/central-endpoint.env) and installed to
`/etc/stayconnect/central-endpoint.env` by `install-central-endpoint.sh`. The ctrlapi unit reads it *after*
`ctrlapi.env`, so the fleet-wide value wins over anything hand-edited on a host.

No runtime component contains a hard-coded Central address any more. The mTLS certificate's names come from
that configuration (`sc-central.echofusion.com`, `150.0.0.252`, `127.0.0.1`), and ctrlapi re-issues a stored
certificate that does not cover them all. Central's public TLS certificate is issued by
`central-mint-tls.sh` from the same source and now carries the FQDN alongside the names already in use.

**The appliance still dials `https://150.0.0.252`.** That is deliberate: see the DNS blocker below.
The mTLS base is `:9443` — the product's real port. It read `:8443` in configuration and provisioning, which
nothing has ever listened on.

## Trust material

Three separate roots, none interchangeable:

| Root | Where | Fingerprint |
|---|---|---|
| Vendor signing (licences, offline packages) | private on Central only; public pinned on the appliance | `L738veIJ67U` / licence key_id `2fbdfcbde209ebb5` |
| Assignment registry root (which keys may sign an assignment) | public pinned on the appliance | `hGVXZ_mDT6I` |
| Central CA (TLS) | appliance system trust store | `O=StayConnect, CN=StayConnect Internal CA` |

The vendor identity **already existed** and was preserved, not rotated — minting a new one would have
invalidated everything already signed by it. Its encrypted escrow lives at
`/opt/stayconnect/central/secrets/vendor-escrow/` and has been proven restorable (a restore into a throwaway
location reproduced the same fingerprint). The passphrase is at `/root/.vendor-escrow-passphrase`, root-only.

> **Escrow is not finished.** Both the encrypted file and its passphrase are on the host they protect. Move
> the file off-host, and keep the passphrase somewhere separate from it.

## Database

Ledgered at `schema_migrations`, 44 rows: 43 adopted (schema verified object-by-object through 0043 before
adoption) and `0044_offline_activation_requests` applied by `central-migrate.sh`. A verified `pg_dump -Fc`
was taken first at `/opt/stayconnect/backups/pre-0044-20260821T135204Z/` and test-restored.

## Kea before LAN configuration

`waiting`, with the reason, and boot convergence treats it as satisfied. Before this change the same
appliance reported `kea = failed` with `converged=false, alert_open=true, pending={kea}` — permanently.
The Kea unit remains `inactive` and `disabled`; nothing is started to make health green. Once a guest
network is applied and confirmed, Kea is checked normally and a real failure is reported as a failure.

## Next step

Manual Product-Owner **Online Activation**: Pending appliance → Customer / Site / licence → Activate. The
appliance is registered, its assignment agent is running, and it is waiting for exactly that.

## Genuine remaining blocker — DNS (two parts)

**`sc-central.echofusion.com` still returns NXDOMAIN.** Re-verified 2026-08-21 after the record was
reported published:

```
dig @150.0.0.11 sc-central.echofusion.com A   ->  status: NXDOMAIN
dig @8.8.8.8    sc-central.echofusion.com A   ->  status: NXDOMAIN
dig @172.21.60.1 …                            ->  timed out (not a working resolver)
dig @150.0.0.11 echofusion.com SOA            ->  launch1.spaceship.net. ... 1782408490
dig @150.0.0.11 echofusion.com NS             ->  launch1.spaceship.net. launch2.spaceship.net.
dig @150.0.0.11 csr-dc-01.coralsearesorts.com ->  150.0.0.11        (control: the server works)
```

### 1. The record does not exist anywhere the appliance can see

**150.0.0.11 is not authoritative for `echofusion.com`.** It forwards to Spaceship, and that zone's SOA
serial `1782408490` is unchanged from before the record was said to be added — so the public zone has not
changed, and there is no internal zone for the name either. A record at Spaceship would also be wrong on its
own: it would have to name a publicly-routable address, and Central is at `150.0.0.252`.

**What to create**, on the internal DNS server 150.0.0.11 (Windows DNS):

> A **primary zone named `sc-central.echofusion.com`**, containing a single **A record at the zone apex**
> (leave the name field blank / `@`) → **`150.0.0.252`**.

Naming the zone after the full host shadows *only* that one name internally; a zone called `echofusion.com`
would shadow the entire public domain for every internal client, including mail and anything else under it.

### 2. The appliance does not use 150.0.0.11 as a resolver

```
resolvectl status  ->  DNS Servers: 172.21.60.1 8.8.8.8     (Current: 8.8.8.8)
```

Publishing the record on 150.0.0.11 alone will therefore not help this appliance: it asks 172.21.60.1
(currently timing out) and 8.8.8.8 (public, no such name). Either point the appliance at a resolver that
serves the zone, or have 172.21.60.1 forward it. That is a topology decision, not a product one — but the
cutover gate will not pass until whichever resolver the appliance actually uses returns the record.

Until both parts are done the appliance stays on `https://150.0.0.252`. Everything else — Central
configuration, both certificates, all three trust roots, the firewall, the schema — is already on the FQDN
and needs no further change.

### Fixed while attempting the cutover

**Central's mTLS port was firewalled.** ufw allowed 22 and 443 only, so ctrlapi's appliance mutual-TLS
listener on 9443 was bound and unreachable — `ss` showed it, loopback tests passed, and every appliance got
a connection timeout. Nothing on Central logs a connection that never arrives. Now open via
`deploy/scripts/central-firewall.sh`, and verified from the appliance: port reachable, certificate carries
`DNS:sc-central.echofusion.com` and matches the hostname. (It chains to the *appliance* PKI, not the Caddy
Internal CA — separate chains by design.)

**The cutover gate's own DNS check was reading `/etc/hosts`.** See the commit; it now queries the upstream
nameservers directly and reports which one answered.

**The cutover itself is now tooling, not a manual edit**:
`deploy/scripts/appliance-central-cutover.sh --check` verifies real DNS (bypassing `/etc/hosts`), HTTPS with
certificate verification on, and that the mTLS listener presents a certificate valid for the name; `--apply`
then switches `SCD_CTRLAPI_BASE`/`SCD_MTLS_BASE`, removes the `/etc/hosts` stopgap, restarts scd, and rolls
everything back if the appliance does not reach Central afterwards. It refuses today, with the reason.

### The `/etc/hosts` stopgap is still in place, deliberately

`150.0.0.252 sc-central.echofusion.com` on the appliance. It was not removed: with DNS still NXDOMAIN,
removing it and switching to the name would cut the appliance off from Central silently — registration and
heartbeat stop, guests are unaffected, and nobody notices until a site goes dark in the fleet view.
