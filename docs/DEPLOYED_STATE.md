# Deployed state — activation, licensing, Central FQDN, Kea health

Current as of **2026-08-21** for the PR #23 deployment this document describes, with a post-closure
deployment table appended on **2026-08-24**. Status: **PRE-LIVE**. No Go-Live, no payment traffic.

> **Scope note (2026-08-24).** The header sentence originally also said "no guest traffic, no LAN/guest
> VLAN/DHCP configuration, no PMS or payment traffic". A guest network and a PMS Interface have since been
> configured on the appliance, so that sentence is no longer true as written and has been narrowed to what is
> still verifiable here. This document is evidence of the PR #23 deployment; it is not a current-state
> inventory of the appliance, and the work that changed those facts is not recorded in `governance/`.

## What is deployed

Both hosts carry the head of `post-roadmap/activation-fqdn-kea-deploy` (PR #23). The live value is in
`/opt/stayconnect/DEPLOYED_SHA` on each host — that file, not this sentence, is the authority, because a
document cannot state the hash of the commit that contains it.

Two milestones worth naming:

- **`253674c2be15cb11d0f746baf73e2c07f1a4d4f2`** — the activation / licensing / FQDN / Kea deployment,
  verified end to end.
- **`469697a`** — the last commit that changes runtime behaviour (Go source or a systemd unit). Everything
  after it is deployment tooling and documentation: the DNS-gated cutover, the resolver alignment, Central's
  firewall rule and the checks that were fixed along the way. That is why the binaries below, built before
  the head, are still current.

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
| scd | same | `4be7adf` | superseded — see the post-closure table below |
| edged, netd | same | `97a9b9d` | superseded for `edged` — see below; `netd` unchanged |
| hotel-admin bundle | same | `5477a2b` | superseded — see the post-closure table below |

Branch `post-roadmap/activation-fqdn-kea-deploy`, PR #23, **unmerged** by instruction.

### Post-closure corrective deployments (master)

The table above records the PR #23 deployment and stays as written, because that is what it is evidence of.
Appliance components have since been redeployed from **`master`** by post-closure corrections, so the rows
above are no longer the current artefacts for those components.

`/opt/stayconnect/DEPLOYED_SHA` on the appliance reads `7c94d6cf58abec603b2e0555d000e6b9c8294ac7`. It is the
PR #23 marker and was NOT advanced by the corrective deployments below, so for these components it is stale;
the binary digests are the reliable identifier.

| Component | Deployed from | Artefact (sha256, first 16) | Landed |
|---|---|---|---|
| `edged` | `883b1f78ea4c8ce906f5d64f5a6bf255d64e765c` (PR #39) | `b9d03ddb2fe17f38` | 2026-08-23 |
| hotel-admin bundle | `883b1f78ea4c8ce906f5d64f5a6bf255d64e765c` (PR #39) | release `20260823-214120` | 2026-08-23 |
| `pmsd` | `4d391cd28be18805e01bb213c970315ac26bc9ae` (PR #36) | `878e8e719e5b0baf` | 2026-08-23 |
| `scd` | `9ec156ea439963d2e5323b6f757c9b9fa3f680c3` (PR #32) | `694da112b0ba57e7` | 2026-08-23 |
| `portald` | `9ec156ea439963d2e5323b6f757c9b9fa3f680c3` (PR #32) | `b9d0a232dfe8f0f4` | 2026-08-23 |
| `netd`, `acctd` | unchanged since PR #23 | `86ec4d1e26b196c0`, `39caa17e6f2f7615` | — |

Hotel Admin is rebuilt with `NEXT_PUBLIC_PHASE2_ADMIN=1` and `NEXT_PUBLIC_PHASE3_ADMIN=1`; 4, 5 and 6 stay
unset. Those flags are inlined at build time, so the parity is verified against the previous bundle before
each deployment rather than assumed.

**This section covers only the components PR #39 deployed plus the digests read from the appliance at the
same time. It is not a full reconciliation of the appliance against `master`** — the wider onboarding work
that has happened since PR #23 is not recorded anywhere in `governance/`, and inventing that record is a
Product-Owner matter, not a documentation edit. See "Real blocker" in the PR #39 synchronization report.

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

## Activation — complete

Activated by the Product Owner 2026-08-21T15:21:45Z. Full chain verified on the appliance:

| | |
|---|---|
| identity | `c6faf4eb-33e4-41b3-9fcb-d902568cc1c9`, serial `SC-7A8M-WM9R-KMAZ` |
| client certificate | `O=<site>, OU=<tenant>, CN=<appliance>`, issued by *StayConnect Appliance Intermediate CA v1*, expires 2026-11-19 |
| mTLS | ready, via `sc-central.echofusion.com:9443` |
| signed assignment | **v1 `assigned`** — CSR SHARM / Coral Sea Holiday |
| licence | **Active**, v1, **2000** concurrent guests, valid to 2026-12-01, grace to 2026-12-31 |
| convergence | `activation_status: activated`, `converged=true`, no alert |

Exactly one of each on Central: one appliance, one signed assignment, one active certificate, one licence.

### It did not complete on the first attempt

The certificate bootstrap was a fixed ten-minute window starting at boot. CSR submitted 15:10:46, window
closed 15:20:46, operator issued at 15:21:45 — **fifty-nine seconds too late**. On expiry the caller logged
a warning and returned, ending the certificate lifecycle for the life of the process.

Everything downstream sat behind it: the assignment channel is mTLS-only, so the signed assignment, the
licence and convergence were all blocked on a certificate that was on Central ready to collect. And
`fetchAssignment` returned silently when mTLS was not ready, so twenty-five minutes of polling wrote nothing
at all — the activation appeared to hang with no error anywhere.

Fixed in `62b412b`: the bootstrap retries with backoff until installed, shutdown, or a terminal error, and
**collects before it submits** so retries never pile up duplicate CSRs. Activation is a human action with no
deadline; an appliance installed on Monday and activated on Friday now converges. The corrected binary
collected the waiting certificate and produced a `delivered` event with **no new `csr_submitted`** — the
idempotence proof.

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
