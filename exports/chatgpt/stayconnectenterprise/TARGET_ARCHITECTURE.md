# Target Architecture — Edge-First Refactor

> Authoritative design for the cloud-controlled / hotel-local split. Companion docs:
> [CLOUD_ARCHITECTURE.md](CLOUD_ARCHITECTURE.md), [EDGE_ARCHITECTURE.md](EDGE_ARCHITECTURE.md),
> [DATA_OWNERSHIP.md](DATA_OWNERSHIP.md), [LICENSING_AND_ENTITLEMENTS.md](LICENSING_AND_ENTITLEMENTS.md),
> [SYNC_PROTOCOL.md](SYNC_PROTOCOL.md), [OFFLINE_OPERATION.md](OFFLINE_OPERATION.md).
> The pre-refactor system is described in [SYSTEM_OVERVIEW.md](SYSTEM_OVERVIEW.md).

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

The hotel's guest WiFi must work with the internet, the cloud, and NATS all down.
Everything a guest or a hotel operator touches runs **on the appliance against a
site-local database**. The cloud keeps only the commercial and fleet-management
domain: who the customers are, what they bought (CommercialPlan), which sites and
appliances exist, signed licenses, and aggregated non-PII telemetry. The appliance
opens **outbound connections only** — nothing in the cloud ever needs to reach into
a hotel network.

## 2. Hierarchy

```
                     ┌──────────────────────────┐
                     │        PLATFORM          │  StayConnect (vendor)
                     │  platform operators,     │  cloud DB, vendor signing key,
                     │  commercial plans,       │  fleet view, platform audit
                     │  license issuance        │
                     └────────────┬─────────────┘
                                  │ 1..n
                     ┌────────────▼─────────────┐
                     │   TENANT / HOTEL GROUP   │  a hotel chain or brand
                     │  subscription, group     │  (tenants table, cloud)
                     │  operators, group audit  │
                     └────────────┬─────────────┘
                                  │ 1..n
                     ┌────────────▼─────────────┐
                     │      SITE / HOTEL        │  one property
                     │  one signed license,     │  (sites table, cloud;
                     │  one ISOLATED local DB   │   stayconnect_site DB, edge)
                     └────────────┬─────────────┘
                                  │ 1..n (usually 1, or an HA pair)
                     ┌────────────▼─────────────┐
                     │  APPLIANCE (or HA pair)  │  the gateway box/VM
                     │  Ed25519 identity, local │  (appliances table, cloud;
                     │  daemons, guest network  │   the whole edge stack)
                     └──────────────────────────┘
```

## 3. The three products

| Product | Runs | Serves | Data |
|---|---|---|---|
| **Cloud** (`control-plane/` = ctrlapi + `cloud-admin/` UI) | StayConnect's infrastructure, served centrally | Platform + group operators: tenants, sites, appliance inventory, CommercialPlans, subscriptions, license issuance/revocation, fleet health | Cloud Postgres — no guest PII |
| **Edge Appliance** (`data-plane/` = scd, portald, acctd, edged) | On-prem at each hotel, inline on the guest network | Guests (captive portal) and the Hotel Admin API | Site-local Postgres `stayconnect_site` — the entire guest domain |
| **Hotel Admin** (`hotel-admin/` UI) | Served from the appliance itself via Caddy on the **management IP** (e.g. `https://172.21.15.30`) | Hotel staff: guest access plans, vouchers, sessions, PMS, walled garden, payments, local operators, backups | Talks only to the local `/edge/v1` API — works with the cloud down |

Terminology rule used everywhere: **CommercialPlan** = the `plans` table (what
StayConnect sells a tenant, cloud domain); **GuestAccessPlan** = the
`ticket_templates` table (what a hotel sells/grants a guest, edge domain).
Plain "Plan" is banned in code, UI and docs.

## 4. Component diagram (one site)

```
 ┌────────────────────────────── CLOUD ───────────────────────────────┐
 │  cloud-admin UI ──▶ ctrlapi (/cloud/v1)                            │
 │        cloud Postgres · Redis · NATS cluster · Prometheus/Grafana  │
 │  vendor Ed25519 signing key (CTRLAPI_VENDOR_KEY)                   │
 └───────────▲───────────────────────────▲────────────────────────────┘
             │ OUTBOUND ONLY             │ OUTBOUND ONLY
             │ NATS: telemetry.<appl>,   │ HTTPS: GET /v1/appliance/license
             │ hb.*, config.*.pms,       │ (Ed25519 appliance JWT, ≤60s)
             │ scd.<appl>.> RPC          │
 ┌───────────┴───────────────────────────┴────────────────────────────┐
 │                     APPLIANCE (per site / HA pair)                 │
 │                                                                    │
 │   mgmt iface (e.g. 172.21.15.30) ── Caddy ──▶ hotel-admin UI       │
 │                                        └────▶ edged  /edge/v1      │
 │                                                  │                 │
 │   local Postgres `stayconnect_site` ◀────────────┼──── scd ──┐     │
 │   (guests, sessions, vouchers, GuestAccessPlans, │    ▲      │     │
 │    PMS config, payments, audit, sync_outbox,     │  acctd  nft/tc  │
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
- **All edge→cloud traffic is a durable outbox drain** (`sync_outbox`, exactly-once
  landing via `fleet_telemetry_dedupe`). See [SYNC_PROTOCOL.md](SYNC_PROTOCOL.md).
- **Guest PII never syncs to the cloud.** Telemetry is aggregates only; the cloud
  ingest additionally strips PII-looking keys (defense in depth, `fleet.Sanitize`).

## 5. API namespaces

| Namespace | Where | Domain |
|---|---|---|
| `/cloud/v1/*` | ctrlapi (cloud) | tenants, sites, appliances, commercial-plans, operators, appliance-bootstrap-tokens, fleet, licenses |
| `/edge/v1/*` | edged (per appliance, mgmt IP) | health, license, operators, guest-access-plans, voucher-batches, vouchers, sessions, pms-providers, auth-methods, walled-garden, portal-branding, payments, stripe-accounts, notification-providers, social-providers, audit, reports, backups |
| `/v1/*` (legacy, ctrlapi) | ctrlapi | **Deprecated** compatibility adapters, removed after the pilot cutover — see [API_DEPRECATIONS.md](API_DEPRECATIONS.md). Appliance protocol endpoints (`/v1/appliances/enroll`, `/v1/appliance/hello`, `/v1/appliance/license`) stay. |

## 6. High availability (per site)

**Support status (truthful):** **single-appliance local-first / offline operation is current and
supported.** **HA failover under the final two-NIC architecture is NOT yet designed, implemented,
or accepted.** The VRRP (keepalived) + conntrackd + NATS nft-set replication + Postgres streaming
replication ideas below are **design intent only**; the earlier design assumed a **dedicated third
HA-sync NIC**, which the approved **two-NIC (WAN+LAN)** rule removes, so the synchronization
**transport is an OPEN architecture decision**. **Do not claim any WAN/LAN HA failover, conntrack
replication, nft replication, or Postgres streaming replication is available** — none is
implemented or accepted under the two-NIC design.

**Known limitation (documented, accepted for now):** a two-node pair has no
quorum. If the HA sync link fails while both nodes are up, both can believe they
are primary (split-brain) and the two local DBs diverge. Recommended mitigation —
use the **cloud heartbeat as a witness/fencing arbiter**: a node that has lost
both its peer *and* its cloud heartbeat acknowledgment should refuse promotion.
This witness role is a design recommendation, not yet implemented.

## 7. Deployment topologies

- **Pilot:** one VM hosts both cloud and one edge. The two live in **separate
  databases within the same Postgres instance** (`stayconnect` vs
  `stayconnect_site`) with **separate DSNs and credentials** — isolation is
  per-database, and moving to physically separate machines later is a
  deploy-topology change only, not a code change.
- **Production:** cloud and appliances are physically separate
  ([DEPLOYMENT_CLOUD.md](DEPLOYMENT_CLOUD.md), [DEPLOYMENT_APPLIANCE.md](DEPLOYMENT_APPLIANCE.md)).
  Each appliance has **exactly two physical NICs**: a **WAN interface that is also the
  management interface** (Hotel Admin, SSH, outbound sync) and a **LAN guest-gateway
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
