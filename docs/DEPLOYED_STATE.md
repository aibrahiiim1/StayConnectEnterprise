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

**The appliance dials `https://sc-central.echofusion.com`** — cut over 2026-08-21T15:10:45Z with
`appliance-central-cutover.sh --apply`, after DNS was published on the FortiGate (172.21.60.1). mTLS is
`sc-central.echofusion.com:9443` — the product's real port; configuration and provisioning read `:8443`,
which nothing has ever listened on.

The `/etc/hosts` stopgap is **gone** (backup at `/etc/hosts.pre-cutover.20260821T151045Z`). Resolution is
now genuine DNS: `resolvectl` reports *"acquired via protocol DNS, link ens160"* rather than the
*"Data from: synthetic"* that marks a hosts-file answer. Rollback: `--rollback`, or
`/etc/stayconnect/scd.env.pre-cutover.20260821T151045Z`.

Verified after the switch, with certificate verification on: `/healthz` and `/readyz` both 200 by name,
mTLS port reachable by name, `identity loaded` and `assignment: agent started` against
`ctrl_base=https://sc-central.echofusion.com`, and a Central-side heartbeat at 15:11:16Z — after the
cutover, so it is the new endpoint being used.

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
appliance is registered by name, its assignment agent is running against
`https://sc-central.echofusion.com`, and it is waiting for exactly that.

No blocker remains. Both parts of the DNS problem are closed.

## DNS — resolved

`sc-central.echofusion.com` → `150.0.0.252`, published on the **FortiGate (172.21.60.1)**, which is the
appliance's resolver. Confirmed on the Production appliance itself, querying that server directly rather
than the local stub.

### The appliance's second resolver had to go first

It was configured with `172.21.60.1` **and** `8.8.8.8`. The FortiGate serves the name; 8.8.8.8 returns
NXDOMAIN, because the name is internal.

That is not a harmless spare. systemd-resolved falls back on a timeout or SERVFAIL — "no answer" — but
NXDOMAIN is a *real* answer, so it is accepted and returned. `resolvectl` was reporting
`Current DNS Server: 8.8.8.8` at the time. The appliance would have resolved Central or not depending on
which server resolved happened to be using, switching on its own: registration and heartbeat stopping and
restarting for no visible reason, guests unaffected, and nobody able to reproduce it.

`appliance-dns-align.sh --apply` removed 8.8.8.8 from both netplan files — commented rather than deleted,
with the reason and date — after first confirming the FortiGate resolves public names, since apt, container
pulls and certificate issuance depend on that. It verifies the internal name and public DNS afterwards and
rolls back if either breaks. The appliance now uses `172.21.60.1` alone.

### Also fixed to make the cutover possible

**Central's mTLS port was firewalled.** ufw allowed 22 and 443 only, so ctrlapi's appliance mutual-TLS
listener on 9443 was bound and unreachable — `ss` showed it, loopback tests passed, and every appliance got
a connection timeout. Nothing on Central logs a connection that never arrives. Now open via
`deploy/scripts/central-firewall.sh`, and verified from the appliance: port reachable, certificate carries
`DNS:sc-central.echofusion.com` and matches the hostname. (It chains to the *appliance* PKI, not the Caddy
Internal CA — separate chains by design.)

**Three checks that reported success while being wrong**, each fixed in source:

- The gate's DNS check queried the local stub, and on systemd-resolved the stub answers from `/etc/hosts` —
  so it certified "resolves in DNS" for a name every real nameserver denied. It now queries the upstream
  servers and names which one answered.
- `sc_dns_lookup` returned its results by printing into a command substitution, which runs in a subshell, so
  the caller read nothing. It sets globals now.
- `dig | grep -q` under `set -o pipefail`: grep exits at the first match, dig gets SIGPIPE, and the pipeline
  reports failure — so a resolver answering 8/8 was rejected. Every such pipeline now captures first. The
  worst instance was the post-cutover verification, which would have rolled back a cutover that worked.

**The cutover is tooling, not a manual edit**: `appliance-central-cutover.sh --check` verifies real DNS
(never the stub), that *every* configured resolver agrees, HTTPS with certificate verification on, and that
the mTLS listener presents a certificate valid for the name. `--apply` switches both bases from the
versioned config, removes the `/etc/hosts` stopgap only after DNS is proven to agree with it, restarts scd,
and rolls everything back unless the appliance both reaches Central and reports `identity loaded`.
