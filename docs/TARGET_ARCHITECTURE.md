# Target Architecture — Edge-First Refactor

> Authoritative design for the cloud-controlled / hotel-local split. Companion docs:
> [CLOUD_ARCHITECTURE.md](CLOUD_ARCHITECTURE.md), [EDGE_ARCHITECTURE.md](EDGE_ARCHITECTURE.md),
> [DATA_OWNERSHIP.md](DATA_OWNERSHIP.md), [LICENSING_AND_ENTITLEMENTS.md](LICENSING_AND_ENTITLEMENTS.md),
> [OFFLINE_OPERATION.md](OFFLINE_OPERATION.md). Central is specified by
> [CENTRAL_CONTROL_PLANE.md](CENTRAL_CONTROL_PLANE.md), which wins over this file wherever they differ.
> The pre-refactor system is described in [SYSTEM_OVERVIEW.md](SYSTEM_OVERVIEW.md) (historical).

> **⚠️ Corrections (2026-07-16):**
> 1. **Appliance topology — approved two-NIC rule.** The appliance has **exactly two physical
>    NICs: WAN and LAN.** **WAN is also the management interface;** **LAN** is the guest gateway
>    (incl. VLAN trunk). The "separate management interface + guest interface + optional HA-sync
>    interface" wording in §3/§4/§6/§7 below is **superseded**: management is the WAN NIC, and
>    there is **no approved third HA-sync NIC** (see `SYSTEM_OVERVIEW.md` WAN=`ens160`/LAN=`ens192`,
>    `STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md`, `DEPLOYMENT_APPLIANCE.md` §1/§7).
> 2. **HA-sync transport is an OPEN architecture decision** under two NICs (the old design assumed
>    a dedicated third NIC). Do not claim a WAN/LAN HA transport is implemented.
> 3. **§8 status table is a dated 2026-07-11 snapshot** — several "in progress" items shipped in
>    later phases (edge-first refactor, Phase 19 networking, IAM Phase 0). Treat that table as
>    historical; the current authoritative status lives in the IAM Phase-0 contract, handoff, and
>    Phase-1A plan.

## 1. Design goal

The hotel's guest WiFi must work with the internet and the cloud down.
Everything a guest or a hotel operator touches runs **on the appliance against a
site-local database**. The cloud (Central) keeps only licensing, activation and
fleet status: who the customers are, which sites and appliances exist, whether
each appliance is activated and connected, and its signed licence (CLAUDE.md
§0E). The appliance has no telemetry subsystem at all. The appliance opens **outbound HTTPS
connections only** — nothing in the cloud ever needs to reach into a hotel
network.

## 2. Hierarchy

```
                     ┌──────────────────────────┐
                     │        PLATFORM          │  StayConnect (vendor)
                     │  platform operators,     │  cloud DB, vendor signing key,
                     │  activation,             │  fleet status, platform audit
                     │  license issuance        │
                     └────────────┬─────────────┘
                                  │ 1..n
                     ┌────────────▼─────────────┐
                     │  CUSTOMER / HOTEL GROUP  │  a hotel chain or brand
                     │  sites, customer users,  │  (tenants table, cloud)
                     │  customer activity       │
                     └────────────┬─────────────┘
                                  │ 1..n
                     ┌────────────▼─────────────┐
                     │      SITE / HOTEL        │  one property
                     │  one ISOLATED local DB   │  (sites table, cloud;
                     │  per appliance           │   stayconnect_site DB, edge)
                     └────────────┬─────────────┘
                                  │ 1..n (usually 1, or an HA pair)
                     ┌────────────▼─────────────┐
                     │  APPLIANCE               │  the gateway box/VM
                     │  Ed25519 identity, one   │  (appliances table, cloud;
                     │  signed license, daemons │   the whole edge stack)
                     └──────────────────────────┘
```

## 3. The three products

| Product | Runs | Serves | Data |
|---|---|---|---|
| **Cloud / Central** (`control-plane/` = ctrlapi + `cloud-admin/` UI) | StayConnect's infrastructure, served centrally | Platform operators and customer users: customers, sites, appliance activation and lifecycle, license issuance/suspension/revocation, fleet status | Cloud Postgres — no guest PII |
| **Edge Appliance** (`data-plane/` = scd, portald, acctd, edged) | On-prem at each hotel, inline on the guest network | Guests (captive portal) and the Hotel Admin API | Site-local Postgres `stayconnect_site` — the entire guest domain |
| **Hotel Admin** (`hotel-admin/` UI) | Served from the appliance itself via Caddy on the **management IP** (e.g. `https://172.21.15.30`) | Hotel staff: guest access plans, vouchers, sessions, PMS, walled garden, payments, local operators, backups | Talks only to the local `/edge/v1` API — works with the cloud down |

Terminology rule used everywhere: **GuestAccessPlan** = the edge
`ticket_templates` table (what a hotel sells/grants a guest). The cloud-side
**CommercialPlan** (`plans`) is retired — the signed appliance licence is the
only entitlement; the old tables were archived by Central migration 0046 and
dropped with the `legacy_archive` schema by migration 0047. Plain
"Plan" is banned in code, UI and docs.

## 4. Component diagram (one site)

```
 ┌────────────────────────────── CLOUD ───────────────────────────────┐
 │  cloud-admin UI ──▶ ctrlapi (/cloud/v1, /v1/auth)                  │
 │        cloud Postgres · Redis · Prometheus/Grafana                 │
 │  vendor, assignment and registry signing keys · appliance CA       │
 └────────────────────────────────▲───────────────────────────────────┘
                                  │ OUTBOUND ONLY — HTTPS, licensing only
                                  │ register · assignment · certificate ·
                                  │ license · hello  (Ed25519 appliance
                                  │ JWT, then mutual TLS)
 ┌───────────┴───────────────────────────┴────────────────────────────┐
 │                     APPLIANCE (per site / HA pair)                 │
 │                                                                    │
 │   mgmt iface (e.g. 172.21.15.30) ── Caddy ──▶ hotel-admin UI       │
 │                                        └────▶ edged  /edge/v1      │
 │                                                  │                 │
 │   local Postgres `stayconnect_site` ◀────────────┼──── scd ──┐     │
 │   (guests, sessions, vouchers, GuestAccessPlans, │    ▲      │     │
 │    PMS config, payments, audit,                  │  acctd  nft/tc  │
 │    tenant_effective_limits ← signed license)     │           │     │
 │                                                  │           │     │
 │   local PMS (FIAS TCP / Mews / Apaleo REST) ◀── scd          │     │
 │                                                              │     │
 │   guest iface (e.g. 10.20.0.1) ── Kea DHCP · Unbound DNS     │     │
 │        │  nftables captive DNAT ──▶ portald ──unix──▶ scd ───┘     │
 │        ▼                                                           │
 │   Guest devices              [HA sync: SUPERSEDED third-NIC design; │
 │                               transport OPEN, not implemented — §6]  │
 └────────────────────────────────────────────────────────────────────┘
```

Key invariants:

- **The guest path never leaves the box.** Voucher, OTP, PMS and social auth,
  concurrency checks, shaping, quotas and accounting all read/write the local DB.
  (External providers — Twilio/SendGrid/Google/Stripe — are internet dependencies
  by nature; see [OFFLINE_OPERATION.md](OFFLINE_OPERATION.md).)
- **Entitlements are a signed file, not a query.** The appliance verifies the
  Ed25519 vendor-signed license offline and mirrors its limits into the local
  `tenant_effective_limits` table, so existing data-plane limit queries keep
  working unchanged. See [LICENSING_AND_ENTITLEMENTS.md](LICENSING_AND_ENTITLEMENTS.md).
- **No telemetry.** Edge→cloud traffic is only registration, CSR, licence
  fetch, hello, assignment acknowledgement and offline-package reconciliation
  (CLAUDE.md §0E). The telemetry subsystem was removed from the appliance on
  2026-09-27 (appliance migration 0093); its historical design is in
  [SYNC_PROTOCOL.md](SYNC_PROTOCOL.md).
- **Moves stay within the customer.** A new customer means retire →
  factory-clean install → registration → activation; an appliance Central
  deletes after it held a customer never re-registers
  ([CENTRAL_CONTROL_PLANE.md §4](CENTRAL_CONTROL_PLANE.md#4-lifecycle)).
- **Guest PII never reaches the cloud.**

## 5. API namespaces

| Namespace | Where | Domain |
|---|---|---|
| `/cloud/v1/*` | ctrlapi (cloud) | overview, customers, sites, appliances, offline activation, licenses, security alerts, trust, audit, team, customer users, backup health — [CENTRAL_CONTROL_PLANE.md §6](CENTRAL_CONTROL_PLANE.md#6-api-contract) |
| `/edge/v1/*` | edged (per appliance, mgmt IP) | health, license, operators, guest-access-plans, voucher-batches, vouchers, sessions, pms-providers, auth-methods, walled-garden, portal-branding, payments, stripe-accounts, notification-providers, social-providers, audit, reports, backups |
| `/v1/*` (ctrlapi) | ctrlapi | Only `/v1/auth/*` (operator session) and the appliance protocol (`/v1/appliances/register`, `/v1/appliance*`). The legacy adapters are removed — see [API_DEPRECATIONS.md](API_DEPRECATIONS.md). |

## 6. High availability (per site)

**Support status (truthful):** **single-appliance local-first / offline operation is current and
supported.** **HA failover under the final two-NIC architecture is NOT yet designed, implemented,
or accepted.** The VRRP (keepalived) + conntrackd + nft-set replication + Postgres streaming
replication ideas below are **design intent only** (the earlier NATS-based nft-set replication was
removed from scd with the rest of its message-bus transport); the earlier design assumed a **dedicated third
HA-sync NIC**, which the approved **two-NIC (WAN+LAN)** rule removes, so the synchronization
**transport is an OPEN architecture decision**. **Do not claim any WAN/LAN HA failover, conntrack
replication, nft replication, or Postgres streaming replication is available** — none is
implemented or accepted under the two-NIC design.

**Known limitation (documented, accepted for now):** a two-node pair has no
quorum. If the HA sync link fails while both nodes are up, both can believe they
are primary (split-brain) and the two local DBs diverge. Recommended mitigation —
use a **cloud witness as a fencing arbiter**: a node that has lost both its
peer *and* its witness acknowledgment should refuse promotion. This witness
role is a design recommendation, not yet implemented (and Central currently
serves appliances for licensing only, CLAUDE.md §0E).

## 7. Deployment topologies

- **Pilot:** one VM hosts both cloud and one edge. The two live in **separate
  databases within the same Postgres instance** (`stayconnect` vs
  `stayconnect_site`) with **separate DSNs and credentials** — isolation is
  per-database, and moving to physically separate machines later is a
  deploy-topology change only, not a code change.
- **Production:** cloud and appliances are physically separate
  ([DEPLOYMENT_CLOUD.md](DEPLOYMENT_CLOUD.md), [DEPLOYMENT_APPLIANCE.md](DEPLOYMENT_APPLIANCE.md)).
  Each appliance has **exactly two physical NICs**: a **WAN interface that is also the
  management interface** (Hotel Admin, SSH, outbound HTTPS to Central) and a **LAN guest-gateway
  interface** (captive network + guest VLAN trunk). There is **no separate management NIC** and
  **no approved dedicated HA-sync NIC** — the HA-sync transport under two NICs is an **OPEN
  architecture decision** (§6).

## 8. Implementation status (2026-07-11 — DATED HISTORICAL SNAPSHOT)

> **Historical snapshot (2026-07-11).** Several "in progress" rows below shipped in later
> phases (edge-first refactor completion, Phase 19 networking, IAM Phase 0 FINAL). This table
> is retained for record; it is **not** the current status. For current status see the IAM
> Phase-0 contract, `StayConnect-IAM-Handoff.md`, and `StayConnect-IAM-Phase1A-Plan.md`.

| Piece | Status |
|---|---|
| `license/` module (sign/verify/store/state machine) | Landed, unit-tested |
| Cloud migration `0019_licensing_fleet` (licenses, fleet_telemetry, dedupe, commercial_plans view) | Landed |
| `internal/licensing` (issuance), `internal/fleet` (ingest), `/cloud/v1` namespace, `/v1/appliance/license` | Landed |
| Edge schema `data-plane/migrations/0001_edge_init` | Landed |
| `edged` daemon, sync-outbox publisher in scd, `cmd/sitemigrate`, `cloud-admin/` + `hotel-admin/` UI split | In progress (design in these docs is authoritative) |
| Update orchestration, support sessions, billing automation, platform sub-roles | Roadmap — not yet implemented |
