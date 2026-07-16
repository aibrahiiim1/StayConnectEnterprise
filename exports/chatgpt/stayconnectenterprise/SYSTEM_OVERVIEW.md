# StayConnect Enterprise — Complete System Overview

> ⚠️ **HISTORICAL SNAPSHOT (generated 2026-07-10) — NOT the current authoritative architecture.**
> This document describes the **pre-refactor system** as of its generation date. It is retained
> as a code-level reference snapshot, not as the canonical current design. For the **current
> authoritative** status and architecture, read (in precedence order):
> [StayConnect-IAM-Phase0-Contract.md](StayConnect-IAM-Phase0-Contract.md) (FINAL,
> Phase 0 CLOSED) → [StayConnect-IAM-Handoff.md](StayConnect-IAM-Handoff.md) →
> [StayConnect-IAM-Phase1A-Plan.md](StayConnect-IAM-Phase1A-Plan.md) →
> [TARGET_ARCHITECTURE.md](TARGET_ARCHITECTURE.md). Note the **approved two-NIC appliance
> topology (WAN=management + LAN=guest)** — which this document already reflects (WAN=`ens160`,
> LAN=`ens192`). Any status/"in progress" tables below are dated snapshots, superseded by the
> authoritative docs above.
>
> ⚠️ **Architecture refactor context — see [TARGET_ARCHITECTURE.md](TARGET_ARCHITECTURE.md); this document describes the pre-refactor system.**
> The edge-first refactor splits this monolith into a Cloud control plane (`/cloud/v1`, commercial + fleet) and per-site Edge appliances (`/edge/v1`, isolated site-local DB, signed offline licensing). Refactor baseline: CURRENT_STATE_ASSESSMENT.md; full doc suite: CLOUD_ARCHITECTURE, EDGE_ARCHITECTURE, DATA_OWNERSHIP, LICENSING_AND_ENTITLEMENTS, SYNC_PROTOCOL, OFFLINE_OPERATION, ROLE_AND_SCOPE_MATRIX, API_DEPRECATIONS, MIGRATION_RUNBOOK, BACKUP_AND_RESTORE, SECURITY_HARDENING, DEPLOYMENT_CLOUD, DEPLOYMENT_APPLIANCE.
> **Phase 19 (edge networking — multi guest-network / VLAN / DHCP management):** see EDGE_NETWORKING.md and its suite (GUEST_VLAN_CONFIGURATION, DHCP_MANAGEMENT, DHCP_OPTION_114, NETWORK_APPLY_AND_ROLLBACK, ARUBA_SSID_VLAN_MAPPING, EXTERNAL_DHCP_MODE, NETWORK_TROUBLESHOOTING).

> Generated 2026-07-10 from a full sweep of the codebase (`d:\WebProjects\StayConnectEnterprise`), the user guides in `docs/user-guide/`, and operational notes from prior work sessions on the pilot VM. This is the "full picture" reference for the whole system.

---

## Table of contents

1. [What the system is](#1-what-the-system-is)
2. [High-level architecture](#2-high-level-architecture)
3. [Network topology of the appliance](#3-network-topology-of-the-appliance)
4. [The guest journey (captive portal flow, end to end)](#4-the-guest-journey-captive-portal-flow-end-to-end)
5. [Data plane — the gateway daemons](#5-data-plane--the-gateway-daemons)
6. [PMS integration (the hotel side)](#6-pms-integration-the-hotel-side)
7. [Control plane — ctrlapi](#7-control-plane--ctrlapi)
8. [Database schema / data model](#8-database-schema--data-model)
9. [Auth model (operators + appliances)](#9-auth-model-operators--appliances)
10. [Guest auth methods in detail (voucher / OTP / social / PMS / paid)](#10-guest-auth-methods-in-detail)
11. [Payments & billing (Stripe)](#11-payments--billing-stripe)
12. [Web admin UI (Next.js)](#12-web-admin-ui-nextjs)
13. [Deployment stack](#13-deployment-stack)
14. [Observability (Prometheus / Grafana / Alertmanager)](#14-observability-prometheus--grafana--alertmanager)
15. [High availability](#15-high-availability)
16. [Testing — the 25 phase E2E suites](#16-testing--the-25-phase-e2e-suites)
17. [Roles & user guides](#17-roles--user-guides)
18. [Live pilot VM & dev workflow](#18-live-pilot-vm--dev-workflow)
19. [Phase history / current status](#19-phase-history--current-status)
20. [Known gaps, gotchas & security notes](#20-known-gaps-gotchas--security-notes)

---

## 1. What the system is

**StayConnect Enterprise** is a Linux-based **inline guest-WiFi hotspot gateway appliance + cloud control plane** for hotels — an enterprise alternative to IACBOX. The appliance sits between the hotel's guest WiFi network and the internet uplink. It:

- Hands out DHCP leases and DNS to guest devices (Kea + Unbound).
- Intercepts unauthenticated traffic with **nftables** and redirects it to a **captive portal**.
- Lets guests authenticate via **voucher codes, email/SMS OTP, social login (Google), PMS room+name lookup, or paid WiFi (Stripe)**.
- Integrates with the hotel's **Property Management System** (FIAS family: Protel/Opera/Fidelio; REST: Mews, Apaleo; plus a dev Stub) to validate that a guest actually has an active reservation.
- Enforces per-session **bandwidth shaping (tc/HTB)**, **data caps and time quotas**, and idle timeouts.
- Reports to a multi-tenant **control plane** (Go API + Postgres/TimescaleDB + Redis + NATS) that a hotel chain's staff manage through a **Next.js admin console** — sites, appliances, vouchers, sessions, PMS providers, walled garden, operators, subscription plans, payments and audit logs.

The whole product is multi-tenant SaaS-shaped: a **platform admin** (StayConnect itself) onboards **tenants** (hotel chains/brands); each tenant has **sites** (properties), each site has **appliances** (gateway boxes/VMs), and tenant staff hold scoped roles.

### Repository layout

| Path | Purpose |
|---|---|
| `control-plane/` | Go API (`ctrlapi`), DB migrations, admin-facing services (cloud or on-prem) |
| `data-plane/` | Gateway daemons that run on the appliance (`scd`, `portald`, `acctd`) |
| `web-admin/` | Next.js 14 admin UI |
| `deploy/` | docker-compose stacks, nftables/Kea/Unbound/netplan/sysctl/systemd/Caddy/observability/HA configs, tc script |
| `docs/` | User guides per role + FIAS 2.20.24 protocol PDF |
| `scripts/` | Bootstrap + 25 phase E2E test suites |
| `Makefile` | infra-up/migrate/build/install targets |

> Note: the README mentions a `policyd` daemon, but **it does not exist** in the tree. Policy is distributed across `shape` (bandwidth), `acctd` (quotas), scd's reaper (expiry/idle), and `pmsguard` (PMS lockout).

---

## 2. High-level architecture

```
                        ┌──────────────────────── CLOUD / CONTROL PLANE ───────────────────────┐
                        │                                                                       │
   Admin browser ──────▶│  web-admin (Next.js :3000) ──/api proxy──▶ ctrlapi (Go, :8080)        │
                        │                                             │        │                │
                        │              Postgres+TimescaleDB :5432 ◀───┘        │                │
                        │              Redis :6379 (operator sessions) ◀───────┤                │
                        │              NATS :4222 JetStream ◀───────────────────┘               │
                        └───────────────────────────────▲───────────────────────────────────────┘
                                                        │ NATS subjects:
                                                        │   hb.{applianceID}          (heartbeat, 10s)
                                                        │   config.{tenantID}.pms     (config push)
                                                        │   scd.{applianceID}.>       (RPC: revoke, pms test/cache/health)
                                                        │   nft.{siteID}              (HA auth-set replication)
                        ┌───────────────────────────────▼──────────── APPLIANCE (per site) ─────┐
                        │                                                                       │
   Guest device ──WiFi──▶ br-lan 10.10.0.1 ── nftables (inet stayconnect) ── ens160 ──▶ Internet│
                        │        │                    │                                        │
                        │   Kea DHCP :67          DNAT :80→8380, :443→8343                      │
                        │   Unbound DNS :53           │                                         │
                        │                             ▼                                        │
                        │   portald :8380/:8343 ──unix socket──▶ scd (/run/stayconnect/scd.sock)│
                        │                                          │  owns nft auth_ipv4 set,   │
                        │   acctd (1s tc tick) ──unix socket───────┤  tc classes, sessions table │
                        │   tc HTB shaping on ens160 + br-lan ◀────┘                            │
                        └───────────────────────────────────────────────────────────────────────┘
```

Key facts:

- **Both planes currently share one Postgres schema.** scd/acctd/voucher/session/otp code read and write the central DB directly (a future phase intends a local SQLite cache + NATS sync split; comments note this as TODO).
- The control plane never talks to appliances synchronously except through the `ApplianceTransport` interface — **NATS request/reply in production, local Unix socket in single-box dev**.
- On the current pilot everything (control plane + data plane + admin + observability) runs on **one VM** (see §18).
- **Caddy** fronts the three public vhosts with TLS: `portal.stayconnect.local` → portald :8380, `api.stayconnect.local` → ctrlapi :8080, `admin.stayconnect.local` → web-admin :3000.

---

## 3. Network topology of the appliance

| Role | Interface | Address |
|---|---|---|
| WAN (uplink / management) | `ens160` | `172.21.60.23/24` (pilot; gw `172.21.60.1`, DNS 1.1.1.1/8.8.8.8) |
| LAN bridge (guests) | `br-lan` | `10.10.0.1/24` |
| LAN physical | `ens192` | no IP — enslaved to `br-lan` |
| HA gateway VIP (VRRP) | `br-lan` | `10.10.0.1` shared |

- Guest DHCP pool: **10.10.0.100 – 10.10.0.250**, lease 3600s (renew 900 / rebind 1800), router + DNS = 10.10.0.1, domain `stayconnect.local` (Kea, memfile leases at `/var/lib/kea/kea-leases4.csv`).
- **Netplan** (`deploy/netplan/02-lan-bridge.yaml`): `br-lan` bridges `[ens192]`, STP off, forward-delay 0.
- **Sysctl** (`deploy/sysctl/99-stayconnect.conf`): IPv4+IPv6 forwarding on, send_redirects off, log_martians on, `nf_conntrack_max = 262144`, tcp_tw_reuse, tcp_fin_timeout 15.
- **Unbound** listens on 10.10.0.1:53 (guests + localhost only, refuse everything else). Serves `portal./captive./gw.stayconnect.local → 10.10.0.1` authoritatively; no wildcard DNS hijack — the captive intercept is purely nftables. Hardened (hide-identity/version, DNSSEC harden flags), prefetch, cache 30s–86400s.

### nftables ruleset (`deploy/nftables/stayconnect.nft`, table `inet stayconnect`)

Sets:
- **`auth_ipv4`** — `ipv4_addr; flags timeout`. The set of *authenticated guest IPs*; owned by scd; each element gets a timeout equal to the session's remaining life. **Membership in this set is what "logged in" means at the kernel level.**
- **`walled_garden_ip`** — `ipv4_addr; flags interval; auto-merge`. Pre-auth reachable IPs/CIDRs (seeded 1.1.1.1, 8.8.8.8, 8.8.4.4); managed from the control plane's walled-garden rules.

Chains:
- **input** (policy drop): lo; established/related; WAN `ens160` allows SSH 22, ICMP echo, and (dev only, "restrict later") ctrlapi 8080 + web-admin 3000; LAN `br-lan` allows DHCP 67/68, DNS 53 udp+tcp, portal 8380+8343, ICMP echo.
- **forward** (policy drop): established/related; `br-lan → ens160` allowed when `saddr @auth_ipv4` (authed) **or** `daddr @walled_garden_ip` (pre-auth walled garden).
- **prerouting_nat**: unauthenticated (`saddr != @auth_ipv4`) and non-walled-garden traffic — `tcp dport 80 → dnat 10.10.0.1:8380`, `tcp dport 443 → dnat 10.10.0.1:8343`. This is the captive redirect.
- **postrouting_nat**: masquerade `10.10.0.0/24` out `ens160`.

### Captive-portal detection per client OS

- **Windows (NCSI)**: probes `http://www.msftconnecttest.com/connecttest.txt` — the DNAT catches it, portald 302s, "Sign in to network" pops. Works out of the box.
- **iOS 16+ / newer Android**: HTTPS probes + Private Relay / encrypted DNS can bypass the plain-HTTP intercept; some iOS versions silently fail on the self-signed HTTPS cert.
- **The reliable fix is RFC 8910 (DHCP option 114 `v4-captive-portal`)** announcing a plain-HTTP portal URL (`http://10.10.0.1:8380/`) in the DHCP OFFER. iOS 14+/macOS 11+/Android 11+/Windows 11 then auto-pop the portal with no probe. Confirmed working on the pilot (2026-04-22) — it made iPhones auto-pop on join.
  - ⚠️ **Repo/VM drift**: the checked-in `deploy/kea/kea-dhcp4.conf` does **not** contain the option-114 stanza; it was applied to the live VM's Kea config during the pilot. If you rebuild from the repo, re-add:
    ```json
    "option-data": [ { "name": "v4-captive-portal", "data": "http://10.10.0.1:8380/" } ]
    ```
    Use the plain-HTTP direct-to-portald URL — not HTTPS and not the Caddy vhost (Caddy 308s to HTTPS and the self-signed cert breaks the flow). Existing leases don't pick the option up until renewal — "Forget network" + rejoin to test.
- Older devices: browse any `http://` site (e.g. `http://nossl.com/`) — the DNAT catches it.
- HTTPS-only apps fail with TLS errors until auth completes — inherent to all captive portals.

### ESXi note (pilot runs on VMware)

The **LAN-side portgroup** must have all three vSwitch security settings set to **Accept**: *Promiscuous mode, MAC address changes, Forged transmits*. The Linux bridge sends frames with the bridge/client MACs, which ESXi drops by default — the symptom is Kea's DHCP OFFERs visible in `tcpdump` but never reaching clients (DORA never completes). The WAN portgroup does not need this.

---

## 4. The guest journey (captive portal flow, end to end)

1. **Associate + DHCP**: device joins the guest SSID; Kea leases an IP in 10.10.0.100–250 with router/DNS = 10.10.0.1 (and, on the pilot VM, the option-114 captive URL).
2. **Detection**: the OS either reads option 114 and opens the portal directly, or fires its HTTP probe which nftables DNATs to portald. portald serves probe endpoints:
   - Apple `/hotspot-detect.html`, `/library/test/success.html` → serves the landing page (never "Success") so iOS pops its captive sheet.
   - Google `/generate_204`, `/gen_204` → 302 to the portal.
   - Windows `/connecttest.txt`, `/ncsi.txt` → 302 to the portal.
   - Any other path → serves the landing page.
3. **Landing page** (`portald` templates): a single tabbed HTML page whose JS fetches `/api/auth-methods` (proxied to scd, which reads `tenants.auth_methods`) and renders only the enabled tabs: **Voucher / Email / Phone / Room (PMS) / Social**.
4. **Authenticate**: guest submits one of the methods. portald resolves the client **IP** from `RemoteAddr` (preserved by DNAT) and **MAC** from `/proc/net/arp`, then proxies to scd over the Unix socket. portald holds **no DB connection and no business logic**.
5. **scd commit sequence** (identical for every auth method):
   1. Validate credential (voucher / OTP / social state / PMS lookup).
   2. Check concurrency against `tenant_effective_limits.max_concurrent_devices` (403 `limit_exceeded` if at cap).
   3. `nft add element inet stayconnect auth_ipv4 { <ip> timeout <ttl>s }`.
   4. `shape.AddSession(ip, downKbps, upKbps)` — tc HTB class + fq_codel + u32 filter on both interfaces (rollback nft on failure).
   5. Insert `guests` (upsert by tenant+MAC) and `sessions` rows (rollback nft+tc on failure).
   6. Voucher path also flips the voucher to `active`.
   7. Increment `scd_sessions_started_total{method}`.
6. **Online**: forward chain now passes the IP; browser is redirected to `/success?s=<session>&t=<seconds>`.
7. **Accounting**: acctd snapshots tc byte counters every second, writes `accounting_records` deltas + updates `sessions.bytes_up/down`/`last_activity_at`, and revokes on `quota_bytes` / `quota_time`.
8. **End of session** (whichever comes first):
   - kernel: `auth_ipv4` element timeout expires,
   - acctd: data cap or duration quota → revoke,
   - scd reaper (30s sweep): `expires_at` passed (reason `quota_time`) or idle > 30 min (reason `idle`),
   - guest `/logout`, admin **Disconnect** (control plane → transport → scd revoke),
   - PMS-based sessions are naturally capped to the remaining stay window at creation time.

---

## 5. Data plane — the gateway daemons

Go module `github.com/stayconnect/enterprise/data-plane` (Go 1.25). Deps: chi v5, pgx/v5, nats.go, prometheus client.

### 5.1 `scd` — Session Controller Daemon (the heart)

Owns the nftables `auth_ipv4` set and the `sessions` table. Listens on a **Unix socket** (`/run/stayconnect/scd.sock`, 0660 root:stayconnect) — portald and acctd are its clients. Optional TCP `/metrics` listener (`SCD_METRICS_ADDR`, pilot uses `127.0.0.1:9101`). Version `0.0.3-dev`.

**Routes on the Unix socket:**

| Method | Path | Purpose |
|---|---|---|
| GET | `/v1/health`, `/metrics` | health / Prometheus |
| POST | `/v1/sessions/authorize` | voucher → session |
| POST | `/v1/sessions/authorize-otp` | OTP verify → session |
| POST | `/v1/sessions/authorize-social` | OAuth callback → session |
| POST | `/v1/auth/pms/verify` | PMS room+name → session |
| POST | `/v1/auth/otp/issue` | issue OTP (email/sms) |
| POST | `/v1/auth/social/start` | begin OAuth (state row) |
| POST | `/v1/sessions/revoke` | tear down session |
| GET | `/v1/sessions/status` | active session for an IP |
| POST | `/v1/admin/pms/{name}/test` | PMS connectivity probe |
| GET | `/v1/admin/pms/{name}/cache` | PMS cache dump |
| GET | `/v1/admin/pms/{name}/health` | PMS health |
| GET | `/v1/tenant/auth-methods` | mirror of `tenants.auth_methods` |

**Env vars:** `SCD_SOCKET`, `SCD_DB_URL`, `SCD_TENANT_ID`/`SCD_SITE_ID`/`SCD_APPLIANCE_ID` (legacy identity), `SCD_MAIL_LOG` (`/var/log/stayconnect/otp-mail.log`), `SCD_SMS_LOG` (`otp-sms.log`), `SCD_IDENTITY_DIR` (`/etc/stayconnect/identity`), `SCD_CTRLAPI_BASE`, `SCD_BOOTSTRAP_TOKEN` + `SCD_SERIAL` (enrollment), `SCD_NATS_URL`, `SCD_METRICS_ADDR`, `SCD_OAUTH_STUB_BASE`, `SCD_PMS_STUB_SEED`.

**Identity resolution priority:** identity files on disk → enroll with bootstrap token → legacy env IDs (warn) → exit 2.

**Background loops:** PMS health flush (30s → DB row + Prometheus gauges), session reaper (30s), PMS reload safety loop (10 min), NATS dispatcher + heartbeat (10s), one boot-time signed `/v1/appliance/hello` smoke call.

### 5.2 `portald` — Captive Portal Daemon

Thin browser-facing proxy. Listens `:8380` HTTP and `:8343` HTTPS (HTTPS only if cert exists at `PORTALD_CERT`, default `/etc/stayconnect/tls/portal.crt`). Runs as user `stayconnect` with only `CAP_NET_BIND_SERVICE`. Resolves IP + ARP MAC and forwards everything to scd's socket.

**Full route surface:** `/` (landing), `POST /auth/voucher`, `POST /auth/otp/request`, `POST /auth/otp/verify`, `GET /auth/social/start`, `GET /auth/social/callback`, `GET /api/oauth/stub/authorize` + `POST /api/oauth/stub/authorize-confirm` (dev stub OAuth consent), `POST /auth/pms/verify`, `GET /api/auth-methods`, `GET /success`, `POST /logout`, `GET /status`, the six OS-probe endpoints, and a catch-all serving the landing.

### 5.3 `acctd` — Accounting Daemon

No listener; a 1-second tick loop (env `ACCTD_TICK_SECONDS`). Each tick: snapshot `tc -s class show` counters for every guest class on WAN+LAN → per-IP deltas (counter-reset safe) → insert `accounting_records`, update `sessions` byte counters + `last_activity_at` → enforce quotas by joining `vouchers`→`ticket_templates` (`data_cap_bytes` → revoke `quota_bytes`; elapsed > `duration_seconds` → revoke `quota_time`) via POST to scd's socket. Requires `ACCTD_TENANT_ID` + `ACCTD_APPLIANCE_ID`. Runs as root (needs `tc`).

### 5.4 `scd-enroll-test`

Standalone enrollment/NATS smoke tester: default hello flow, `--replay` (JWT replay-cache check), `--nft-publish` / `--nft-await` (HA set replication testing).

### 5.5 Internal packages (data-plane)

| Package | What it does |
|---|---|
| `applianceauth` | Ed25519 JWT signing for scd→ctrlapi (`alg=EdDSA`, 30s lifetime, random `jti` nonce) |
| `identity` | `identity.json` + `ed25519.key` on disk; `LoadOrEnroll` posts pubkey+bootstrap token+serial to `/v1/appliances/enroll` |
| `mail` | `Mailer` interface — `Stub` (logs to otp-mail.log) and `SendGrid` (v3 API, Bearer, expects 202) |
| `sms` | `Sender` interface — `Stub` and `Twilio` (Messages.json, Basic auth, expects 201). MessageBird mentioned, not implemented |
| `otp` | 6-digit codes, salted-SHA256 at rest, TTL 10 min, 5 verify attempts, 60s per-destination cooldown, 5/dest/hour, 20/IP/hour; single-use, constant-time compare, `FOR UPDATE` tx |
| `phone` | E.164 normalization (`00`→`+`, strip separators, 8–15 digits) |
| `pms`, `pmsloader`, `pmsguard` | See §6 |
| `session` | guests/sessions row management; `CheckConcurrency` against effective limits; `Start` / `StartOTP` / `StartPMS` / `End` / `FindActive` |
| `shape` | tc HTB per-session classes; WAN=upload (src match), LAN=download (dst match); classid minor = `0x1000 + last IP octet` (range `1:1000..1:10ff`); fq_codel leaf; deterministic filter pref; 0 kbps = 1 Gbps uncapped; `Stats()` regex-parses plaintext `tc -s class show` |
| `nft` | idempotent wrapper over `/usr/sbin/nft`: `Allow(ip, ttl)`, `Deny(ip)`, JSON `List()` of `auth_ipv4` |
| `social`, `socialloader` | OAuth providers: **Google** (real OIDC: accounts.google.com auth, token exchange, userinfo; requires verified email) + **Stub**; apple/facebook/microsoft are placeholders that keep the stub |
| `notifyloader` | Resolves tenant's enabled email/SMS provider rows; wraps them with metrics + `last_success_at`/`last_error` DB health writes; falls back to stubs |
| `tenantcfg` | Uncached read of `tenants.auth_methods` JSON (which portal tabs to show, PMS mode/lockout settings) |
| `voucher` | Crockford-normalized code validation/consumption against `vouchers`+`ticket_templates`; returns remaining duration/bytes + bandwidth |
| `metrics` | Prometheus registry, const labels `{tenant_id, site_id, appliance_id}`, all `scd_*` series (sessions, otp, pms, nft ops, reaper, notifications, social) with pre-touched zero counters |

---

## 6. PMS integration (the hotel side)

**Guest login = room number + ONE OF (last name | first name | reservation number)**, mode configurable: `room_lastname`, `room_firstname`, `room_reservation`, `either`.

### Provider kinds

| Kind | Transport | Notes |
|---|---|---|
| `stub` | in-memory | dev/pilot; always "connected" |
| `protel-fias`, `opera-fias`, `fidelio-fias` | **FIAS over persistent TCP** (STX/ETX framing, pipe-separated records) | push-based: maintains an in-memory room cache fed by GI (guest-in) / GC (change) / GO (guest-out) records; handshake LS→LD (`IFPB`, v1.13, RT4)→LR subscriptions; `LA` keepalive after 60s idle; exponential reconnect 1s→30s; dates YYMMDD UTC. FIAS 2.20.24 spec PDF is in `docs/` |
| `mews` | REST (`api.mews-demo.com` default) | ClientToken+AccessToken in body; background room→SpaceId refresh every 15 min; ValidateGuest = reservations/getAll (Processed/Started, colliding now) + customers/getAll |
| `apaleo` | REST + OAuth2 client-credentials (`identity.apaleo.com`) | token cache w/ 60s skew, 401 invalidation; room→unitId map refresh 15 min; reservations OverlappingStay with expand=primaryGuest; reservation number matches `bookingId` |

### Configuration model (`pms_providers` table)

Per **tenant**, optionally overridden per **site** (site-scoped row with the same name wins). Columns include connection (host/port/use_tls/auth_key for FIAS; base_url/api_key/property_id for REST), plus JSON `field_map` (FIAS field-ID mapping, merged over per-kind defaults), `normalization` (room format, strip name titles, reservation case), and `stay_window`:

- **EarlyCheckinMinutes** — grace before check-in (guest in lobby),
- **LateCheckoutMinutes** — grace after check-out,
- **MinRemainingSeconds** — reject if stay ends sooner than this (default 60s).

Name matching normalizes case, whitespace, apostrophes and diacritics (O'Brien / Chloé handled).

### The verify flow (scd `pmsVerify`, 7 steps)

1. `pmsguard.CheckIP` — per-IP rate limit (default 30 attempts / 15 min).
2. `pmsguard.CheckRoom` — per-room lockout (default 5 failures / 15 min) — anti guessing.
3. `provider.ValidateGuest` (errors → `not_found` / `upstream_fail` / `checked_out`).
4. Stay-window check with grace applied (`before_checkin` / `after_checkout` rejects).
5. Load ticket template; **cap session duration to the remaining stay**.
6. Concurrency check.
7. nft + tc + `StartPMS`. Every attempt recorded in `pms_attempts`. All failures return a generic 401 "verification failed" (no oracle for attackers).

### Live reload

- Control plane publishes `config.{tenantID}.pms` on NATS after any PMS provider mutation → scd (`QueueSubscribe` group `scd-reload-pms`, one HA node reloads) rebuilds the provider registry from the DB, starts the new generation, atomically swaps, then stops the old one.
- A **10-minute safety loop** re-runs the reload regardless, guaranteeing eventual consistency.
- **Stub seed gotcha (fix landed 2026-04-22)**: `SCD_PMS_STUB_SEED=true` seeds test reservations (101 Alice Anderson / RES-1001, 102 Bob O'Brien, 103 Chloé Dubois, 201 future guest, 202 past guest). Because every reload builds a *fresh* in-memory Stub, `maybeSeedPMSStubs()` is called from **both** `main()` and `reloadPMS()` — otherwise PMS logins on the pilot would silently break after 10 minutes. Keep that hook if reload code changes.
- Health flush every 30s writes provider status/last_record_at/last_error back to the `pms_providers` row (drives the admin UI status badge) and Prometheus gauges (`scd_pms_provider_status` 0=down…4=idle).

---

## 7. Control plane — ctrlapi

Go module `github.com/stayconnect/enterprise/control-plane` (Go 1.25, version `0.0.2-dev`). Single binary, two subcommands:

- `ctrlapi serve` — the HTTP server on `CTRLAPI_ADDR` (default **:8080**).
- `ctrlapi seed-admin --email --password [--name]` — idempotently creates a `platform_admin` operator (argon2id hash, password ≥ 10 chars).

**Env vars:** `CTRLAPI_ADDR` (:8080), `CTRLAPI_DB_URL`, `CTRLAPI_REDIS_URL` (redis://127.0.0.1:6379/0), `CTRLAPI_NATS_URL` (presence toggles NATS transport; unset → local Unix transport to scd), `CTRLAPI_LOG_LEVEL`, `CTRLAPI_ENV`, `CTRLAPI_COOKIE_SECURE`, `CTRLAPI_ALLOW_ORIGINS` (CORS allowlist, defaults include `http://172.21.60.23:3000`), `CTRLAPI_SCD_SOCKET`.

**Startup:** slog JSON → config → pgx pool (max 10 conns) → Redis ping → transport selection (NATS or Unix) → metrics → heartbeat consumer (NATS only) → hourly bootstrap-token expiry sweeper → OIDC registry (stub provider only) → chi router → graceful shutdown on SIGINT/SIGTERM.

**Middleware chain:** RequestID → RealIP → X-Trace-Id mirror → Logger → Recoverer → Timeout(15s) → CORS (credentialed allowlist) → Prometheus middleware (chi route-pattern labels).

**Uniform conventions:** error envelope `{error, message, trace_id}` (codes: unauthenticated, forbidden, payment_required, not_found, conflict, bad_request, limit_exceeded, bad_gateway, internal); list envelope `{data, meta{has_more, cursor}}` with keyset cursor pagination on `(created_at DESC, id DESC)`; every mutation emits an `audit_log` row (best-effort, with `_tenant_id` threading so platform-admin actions land in the right tenant's log).

### Complete REST API surface

Auth legend: **PUBLIC**, **SESSION** (sc_session cookie), **+TENANT** (tenant scope required), **APPL-JWT** (Ed25519 appliance bearer), **HMAC** (Stripe signature).

**Infra:** `GET /healthz`, `GET /readyz` (DB+Redis ping), `GET /metrics`, `GET /v1/version`, stub-SSO consent pages `GET|POST /oauth/stub/authorize-sso[/confirm]`.

**Auth:** `POST /v1/auth/login`, `POST /v1/auth/logout`, `GET /v1/auth/whoami` (SESSION), `GET /v1/auth/sso/providers?tenant=`, `GET /v1/auth/sso/start`, `GET /v1/auth/sso/callback`.

**Appliance:** `POST /v1/appliances/enroll` (PUBLIC, bootstrap-token gated), `GET /v1/appliance/hello` (APPL-JWT), `GET|POST /v1/appliance-bootstrap-tokens/`, `DELETE /v1/appliance-bootstrap-tokens/{id}` (SESSION+TENANT).

**Payments:** `POST /v1/checkout/create` (PUBLIC guest), `GET /v1/checkout/{session_id}` (PUBLIC poll), `POST /v1/webhooks/stripe/{tenant_id}` (HMAC), `GET /v1/payments/` (SESSION+TENANT).

**Tenants:** CRUD `/v1/tenants[/{id}]` (writes platform_admin; DELETE = soft archive); `GET|POST /v1/tenants/{id}/subscription` (change plan: tenant_admin/billing/super); `GET .../effective-limits`; `GET .../audit` (filters: action CSV, from/to, ≤500, default 7 days); usage analytics `GET .../usage/{timeseries,summary,top-sites,top-appliances}` (TimescaleDB time_bucket).

**Sites:** CRUD `/v1/sites[/{id}]` (SESSION+TENANT, `max_sites` enforced).

**Appliances:** CRUD `/v1/appliances[/{id}]` (`max_appliances`; default status `pending`), `GET /v1/appliances/{id}/effective-config` (resolved PMS providers — site-over-tenant — ∪ walled-garden rules).

**Ticket templates:** CRUD `/v1/ticket-templates[/{id}]` (code immutable; delete 409s if vouchers reference it).

**Vouchers:** `GET|POST /v1/voucher-batches/` (count 1..10000, `max_vouchers_per_month`, bulk `COPY FROM`), `GET .../{id}`, `GET .../{id}/codes`, `GET .../{id}/codes.csv`, `POST .../{id}/revoke`; `GET /v1/vouchers/{id}`, `POST /v1/vouchers/{id}/revoke`.

**Plans:** `GET /v1/plans[/{id}]` (read-only; catalog managed via SQL — no admin UI yet).

**Operators:** CRUD `/v1/operators[/{id}]` (tenant_admin/super; disable-not-delete; can't disable self; `max_operators`), `POST .../{id}/set-password`, `POST .../{id}/roles`, `DELETE .../{id}/roles/{role}` (only super mints platform_admin; can't remove own platform_admin).

**Walled garden:** CRUD `/v1/walled-garden[/{id}]` (kind domain|cidr|ip, ports validated), `GET /v1/walled-garden/effective?site_id=` (tenant ∪ site union).

**Sessions:** `GET /v1/sessions/` (filters state/site/appliance/since/q=ip-or-mac), `GET .../{id}`, `POST .../{id}/disconnect` (→ transport → scd; 409 if not active, 502 if appliance unreachable).

**PMS providers:** CRUD `/v1/pms-providers[/{name}]` (`?site_id=` scoping, secrets write-only, publishes config push), `POST .../{name}/test`, `GET .../{name}/cache`, `GET .../{name}/health` (all proxied live to the appliance via transport).

**Provider admin:** CRUD `/v1/notification-providers` (email: stub/sendgrid/ses; sms: stub/twilio; one enabled per channel), `/v1/social-providers` (google/apple/facebook/microsoft), `/v1/stripe-accounts` (one enabled per tenant; secret_key/webhook_secret write-only).

### Internal packages (control-plane)

| Package | Purpose |
|---|---|
| `config` | env loading |
| `db` | pgx pool |
| `crockford` | voucher codes — Crockford base32 (no I/L/O/U), 12 chars = 60 bits, `XXXX-XXXX-XXXX` display, normalize maps I/L→1 O→0 |
| `auth` | argon2id passwords (m=64MiB t=1 p=4); Redis opaque sessions (`sc:sess:*`, 12h sliding); `RequireAuth`/`RequireRole`/`RequireTenant`/`EffectiveTenantID`; `RequireAppliance` JWT middleware |
| `applianceauth` | Ed25519 JWT verify, 60s max lifetime, in-process jti ReplayCache (2 min / 8192 entries) |
| `audit` | append-only `audit_log` writes, never fails the caller |
| `oidc` | operator SSO abstraction: Claims, Registry, `ClaimsMap.ResolveRoles` (IdP groups → roles), Stub IdP |
| `transport` | `ApplianceTransport` interface (Revoke, PMSTest, PMSCache, PMSHealth) — `NATSTransport` (subjects `scd.{id}.*`, `Nats-Status` header) and `LocalUnixTransport` (HTTP over scd socket) |
| `configpush` | publishes `config.{tenantID}.pms` change events (nil-safe without NATS) |
| `heartbeat` | consumes `hb.*`; promotes enrolled/offline→online + records version; 15s sweeper flips online→offline after 30s silence |
| `metrics` | `ctrlapi_*` Prometheus series (HTTP, heartbeats, appliance flips, config pushes, payment checkout/webhook/amount counters) |
| `stripe` | hand-rolled client: CreateCheckoutSession (inline price_data) + VerifyWebhook (HMAC-SHA256, 5-min tolerance, constant-time) |
| `http` | router, CORS, login/logout/whoami |
| `api` | all business handlers + limit enforcement + cursor pagination |

---

## 8. Database schema / data model

Postgres 16 + **TimescaleDB** (`timescale/timescaledb:2.16.1-pg16`). UUID PKs (`gen_random_uuid()`). 18 migrations (`control-plane/migrations/0001..0018`), applied by the Makefile loop; each records into `schema_migrations`.

### Migration list

| # | Name | Adds |
|---|---|---|
| 0001 | extensions | pgcrypto, uuid-ossp, timescaledb, schema_migrations |
| 0002 | tenants_sites_appliances | tenants, sites, appliances, networks, walled_garden_rules, operators, operator_roles |
| 0003 | tickets_vouchers_guests_sessions | ticket_templates, vouchers, guests, sessions, accounting_records (hypertable), audit_log (hypertable) |
| 0004 | plans_subscriptions | plans, plan_limits, tenant_subscriptions, tenant_limit_overrides, **tenant_effective_limits (VIEW)**, usage_counters (hypertable), subscription_events, invoices, invoice_lines |
| 0005 | seed_default_plans | 6 plans (starter/pro/enterprise × monthly/yearly) + ~17 limit keys each |
| 0006 | voucher_batches | voucher_batches; vouchers.batch_id; global unique on vouchers(code) |
| 0007 | subscription_event_change_type | change_type upgrade/downgrade/lateral |
| 0008 | email_otp | tenants.auth_methods jsonb; guest contact columns; auth_otps; auth feature limit keys |
| 0009 | social_oauth | social_oauth_states |
| 0010 | operator_sso | idp_providers, auth_oidc_states; operators auth_method/oidc_sub |
| 0011 | pms_auth | pms_attempts; feature.auth.pms keys |
| 0012 | pms_provider_config | pms_providers |
| 0013 | appliance_enrollment | appliance_bootstrap_tokens; appliances.identity_verified_at; status enum pending/enrolled/online/offline/retired |
| 0014 | pms_per_site_overrides | pms_providers.site_id + **partial unique indexes** (tenant-wide `WHERE site_id IS NULL` / per-site) — note: `ON CONFLICT` clauses must name the predicate |
| 0015 | sessions_expires_at | sessions.expires_at + partial indexes on active sessions |
| 0016 | notification_providers | notification_providers |
| 0017 | social_oauth_providers | social_oauth_providers |
| 0018 | stripe_payments | stripe_accounts, payments, stripe_events |

### Tables by domain

**Multi-tenancy:** `tenants` (slug unique, status active|suspended|archived, `auth_methods` jsonb — the per-tenant portal method switchboard), `sites` (unique (tenant_id, code), timezone), `appliances` (serial globally unique, status pending|enrolled|online|offline|retired, base64 Ed25519 `public_key`, `last_seen_at`, version), `networks` (ssid/vlan/cidr per appliance; schema only, no API yet), `walled_garden_rules` (kind domain|cidr|ip, ports[], site_id NULL = tenant-wide).

**Operators:** `operators` (email unique, argon2id hash or SSO-only, status active|disabled|invited), `operator_roles` (role ∈ platform_admin|tenant_admin|tenant_operator|viewer|billing; tenant_id NULL for platform), `idp_providers` (per-tenant OIDC config + claims_map), `auth_oidc_states` (SSO CSRF, single-use, IP-bound, 10 min).

**Guest access:** `ticket_templates` (the WiFi "product": duration, data cap, down/up kbps, max_concurrent_devices, price_cents/currency, is_active), `vouchers` (state unused|active|exhausted|expired|revoked; unique (tenant, code) + global code unique), `voucher_batches`, `guests` (unique (tenant, mac); email/phone + verified_at stamps), `sessions` (ip/mac, started/last_activity/ended/expires_at, end_reason quota_bytes|quota_time|admin|idle|dhcp_expired|policy, bytes up/down, state pending|active|suspended|closed), `accounting_records` (hypertable, 1-day chunks), `auth_otps` (hashed codes, attempts, consumed_at), `social_oauth_states` (guest OAuth CSRF bound to IP+MAC), `pms_attempts` (lockout telemetry).

**PMS:** `pms_providers` (kind, connection cols, field_map/normalization/stay_window jsonb, health columns, site_id override, partial uniques).

**Billing:** `plans` + `plan_limits` (typed key/value + unit), `tenant_subscriptions` (one non-terminal per tenant via partial unique; status trialing|active|past_due|canceled|paused), `tenant_limit_overrides` (expiring, reasoned), **`tenant_effective_limits` view** (plan limits merged with overrides — override wins; powers every quota check), `usage_counters` (hypertable), `subscription_events`, `invoices`/`invoice_lines` (schema only, no handlers).

**Payments:** `stripe_accounts` (one enabled per tenant), `payments` (stripe_session_id unique, status pending|paid|failed|expired|cancelled, voucher_id), `stripe_events` (event_id PK = idempotency gate).

**Audit:** `audit_log` (hypertable, 7-day chunks; ts, tenant, actor_type operator|system|appliance|guest|api, action like `site.created`, target, ip, payload jsonb).

### Plan limit keys (seeded)

`max_sites, max_appliances, max_concurrent_devices, max_monthly_active_devices, max_vouchers_per_month, max_operators, max_bandwidth_mbps_per_site, max_bandwidth_gb_per_month, max_ssids_per_site, retention_days_accounting, retention_days_audit, api_rate_limit_rpm` + booleans `feature.sso_saml, feature.pms_integration, feature.ha_pair, feature.api_access, feature.white_label` + auth gates `feature.auth.{voucher,email_otp,sms_otp,social,saml,pms}`. `-1` = unlimited (enterprise). **No subscription at all ⇒ HTTP 402** on limit-gated creates; over limit ⇒ 403 `limit_exceeded` with `{limit_key, limit, current}`.

---

## 9. Auth model (operators + appliances)

### Operators (humans)

- **Opaque Redis-backed session cookies, not JWTs.** `POST /v1/auth/login` → argon2id verify → 32-byte random token in Redis (`sc:sess:<token>`, **12h sliding TTL**) → `sc_session` HttpOnly cookie (SameSite=Lax; Secure per `CTRLAPI_COOKIE_SECURE`).
- **Five roles:** `platform_admin` (global super; scopes into any tenant via `?tenant_id=`), `tenant_admin` (full tenant control incl. staff + plan), `tenant_operator` (day-to-day ops; no staff/plan), `viewer` (read-only), `billing` (view + change plan only). One operator can hold multiple roles.
- **Tenant scoping** (`EffectiveTenantID`): platform admins choose a tenant per request; everyone else is pinned to their `DefaultTenantID` — overrides are silently ignored.
- **Operator SSO (OIDC)**: state+nonce rows in `auth_oidc_states`; callback verifies nonce and `email_verified`, then links by `oidc_sub`, links by email, or auto-provisions with roles derived from IdP groups via `claims_map`. Only the **Stub IdP** is registered in this build (phase 4.4) — real OIDC providers are a config-away.

### Appliances (gateways)

- **Not mTLS.** Each appliance holds a persistent **Ed25519 keypair**; every request to ctrlapi carries a short-lived **Ed25519-signed JWT** (max lifetime 60s, `jti` nonce, replay cache 2 min/8192 entries in-process).
- **Enrollment:** admin mints a single-use **bootstrap token** (32-char base32, stored as SHA-256 + 4-char hint, ≤7-day TTL, optional expected-serial lock) bound to a site → scd posts `{bootstrap_token, serial, public_key}` to `POST /v1/appliances/enroll` → server binds the key, creates/updates the appliance (status `enrolled`), consumes the token. All failures return an opaque "enrollment rejected". Plaintext token is shown **once** in the admin UI (paste into `SCD_BOOTSTRAP_TOKEN` in `/etc/stayconnect/scd.env`).
- **Liveness:** NATS heartbeat every 10s → `online`; 30s silence (~3 missed beats) → sweeper flips `offline`. UI shows Online/Stale/Offline.

---

## 10. Guest auth methods in detail

| Method | Flow | Backing |
|---|---|---|
| **Voucher** | portal form → scd normalizes Crockford code → validate state/expiry/remaining → activate → session with template's duration/cap/bandwidth | `vouchers` + `ticket_templates`; batches generated up to 10k codes, CSV/print export, batch or single revoke |
| **Email OTP** | request → 6-digit code emailed (SendGrid or stub log) → verify → session; stamps `guests.email` + verified_at | `auth_otps`; 10-min TTL, 5 attempts, 60s cooldown, 5/dest/hr, 20/IP/hr |
| **SMS OTP** | same via Twilio (or stub log); phone normalized to E.164 | same |
| **Social** | `/auth/social/start?provider=` → scd creates state row bound to IP+MAC (10 min, single-use) → provider consent → callback → code exchange → requires verified email → session attaches guest by MAC/email | `social_oauth_states`, `social_oauth_providers`; **Google real**, stub fallback for dev; apple/facebook/microsoft placeholders |
| **PMS** | room + name/reservation → guard checks → provider lookup → stay-window → session capped to remaining stay | see §6 |
| **Paid (Stripe)** | portal → `POST /v1/checkout/create` → Stripe hosted checkout → webhook issues a voucher → guest polls `GET /v1/checkout/{id}` for the code → redeems as a voucher | see §11 |

Which tabs appear on the portal is controlled per tenant by `tenants.auth_methods` (JSON: voucher/email/sms/social/pms each with `enabled` + `template_id`; PMS also mode + lockout tuning). Plan feature gates (`feature.auth.*`) exist in the limits system.

---

## 11. Payments & billing (Stripe)

Two separate concepts:

**A. Guest WiFi purchases (implemented, phase 12).**
- Per-tenant `stripe_accounts` row: publishable key (readable), secret key + webhook secret (write-only), success/cancel URLs, one enabled per tenant.
- Checkout: pre-insert `payments` row (pending) → hand-rolled `CreateCheckoutSession` with inline `price_data` from the ticket template and metadata `stayconnect_payment_id`/`stayconnect_tenant_id` → return hosted URL.
- Webhook `POST /v1/webhooks/stripe/{tenant_id}`: HMAC-SHA256 verified (5-min tolerance, constant-time); idempotency via `INSERT INTO stripe_events ON CONFLICT DO NOTHING`; on `checkout.session.completed` + `paid`, one transaction issues a fresh Crockford voucher and flips the payment to `paid`; on voucher-issue failure the event row is deleted so Stripe's retry reprocesses. Failures alert (see §14 — "charged without access, investigate IMMEDIATELY").
- Admin history at `/v1/payments/`; refunds are issued via the admin UI per the user guide.

**B. SaaS platform subscriptions (internal, no Stripe calls).**
- Plan catalog (`plans`/`plan_limits`) seeded by migration; managed by SQL (no UI yet).
- `POST /v1/tenants/{id}/subscription` swaps plans **immediately** (cancels current, inserts new, trial only on first-ever subscription), records `subscription_events` with derived change_type. `external_ref` is reserved for a Stripe subscription id; invoices tables exist but have no handlers.

---

## 12. Web admin UI (Next.js)

`web-admin/` — Next.js **14.2.5**, App Router, React 18.3, Tailwind 3.4 (custom dark palette, brand `#5b8cff`), lucide-react icons, TypeScript strict. No component library, no state/data library — a hand-rolled UI kit (`components/ui`: Button/Card/Badge/Input/Table/EmptyState/ErrorBanner) and per-page `useState`/`useEffect`.

- **Dev/prod port 3000** (`next dev|start -p 3000 -H 0.0.0.0`).
- **API access:** browser calls same-origin `/api/*`; `next.config.mjs` rewrites to `API_UPSTREAM` (default `http://127.0.0.1:8080`). Cookies flow same-origin — no CORS in the happy path.
- **Auth:** login page (`/login`, default prefill `admin@stayconnect.local`, org slug `dev`) posts to `/v1/auth/login`; also lists SSO providers per org slug and links to `/v1/auth/sso/start`. `middleware.ts` redirects on cookie *presence*; the authenticated layout validates via `whoami`. Logout → `/v1/auth/logout`.
- **Tenant resolution:** `useTenant()` — tenant operators use `default_tenant_id`; platform admins fall back to the `dev`-slug tenant (or first tenant). Nearly every request carries `?tenant_id=`.
- **Role gating is server-side only** — the sidebar shows all 14 items to everyone; forbidden actions fail with API errors. Plan-limit errors render uniformly ("Plan limit reached: {key} ({current}/{limit})").
- **Errors** carry `trace_id` for support correlation.

### Pages (route → what it does)

| Route | Section | Contents / actions |
|---|---|---|
| `/dashboard` | Overview | KPIs: active sessions, data this month (with cap %), sessions today, plan + renewal; top-5 sites bar chart. Read-only |
| `/sites` | Infrastructure | list/create/delete sites (code, name, timezone, country) |
| `/appliances` | Infrastructure | list with **live status pulse** (10s tick; green <25s heartbeat), create appliance, **mint/revoke bootstrap tokens** (plaintext shown once, copy button), effective-config drawer (resolved PMS + walled garden), delete |
| `/ticket-templates` | Access | CRUD WiFi products: duration, data cap, down/up kbps, device limit, price; active toggle |
| `/voucher-batches` (+`/[id]`) | Access | create batch (template, count ≤10000), revoke-all, CSV export; detail page lists codes w/ state |
| `/sessions` | Access | Active (10s poll) / Recent tabs; IP/MAC, state, byte counters; **Disconnect** |
| `/pms-providers` | Integrations | full CRUD incl. site-scope overrides, FIAS vs REST field sets, field_map/normalization/stay_window JSON editors, **Test / Health / Cache viewer** live against the appliance |
| `/notifications` | Integrations | email (stub/sendgrid/ses) + SMS (stub/twilio) provider CRUD; health from last_success/error |
| `/social-providers` | Integrations | google/apple/facebook/microsoft OAuth creds CRUD |
| `/payments` | Integrations | Stripe account CRUD (webhook URL hint) + read-only payments table |
| `/subscription` | Billing ("Plan") | current subscription, effective limits table (plan vs override source), plan cards, change-plan with upgrade/downgrade classification |
| `/operators` | Administration | create (email/name/password ≥10/role), disable, reset password, add/remove role badges; self-protection (can't disable self / remove own roles) |
| `/walled-garden` | Policy | rules CRUD: domain/ip/cidr + optional ports + site scope |
| `/audit` | Compliance | last-7-days audit table with action filter; actor, action badge, target, IP, JSON payload |

---

## 13. Deployment stack

### Infra compose (`deploy/compose/infra.yml`, project `stayconnect-infra`)

| Service | Image | Port (127.0.0.1 only) |
|---|---|---|
| postgres | timescale/timescaledb:2.16.1-pg16 | 5432 (user/pass/db `stayconnect`) |
| redis | redis:7-alpine | 6379 |
| nats | nats:2.10-alpine (JetStream `-js`) | 4222 client, 8222 monitor |

### systemd units (`deploy/systemd/` + caddy)

Dependency spine: `nftables` → **`stayconnect-scd`** → `stayconnect-portald` / `stayconnect-acctd`; `stayconnect-tc-setup` (oneshot, before scd); `docker` → `stayconnect-ctrlapi` → `stayconnect-web-admin`; plus `stayconnect-caddy`.

| Unit | Runs | User | Notes |
|---|---|---|---|
| stayconnect-tc-setup | `deploy/scripts/tc-setup.sh` | root | oneshot; primes HTB roots on ens160+br-lan (root `1:` htb, aggregate `1:fffe` @1gbit, default `1:1`+fq_codel); ExecStop tears down |
| stayconnect-scd | `/opt/stayconnect/bin/scd` | root | needs CAP_NET_ADMIN for nft; RuntimeDirectory=stayconnect; env `/etc/stayconnect/scd.env` |
| stayconnect-portald | `/opt/stayconnect/bin/portald` | stayconnect | CAP_NET_BIND_SERVICE only; ProtectSystem=strict |
| stayconnect-acctd | `/opt/stayconnect/bin/acctd` | root | needs tc |
| stayconnect-ctrlapi | `/opt/stayconnect/bin/ctrlapi serve` | root | env `/etc/stayconnect/ctrlapi.env` |
| stayconnect-web-admin | `npm run start` in `/opt/stayconnect/web-admin` | root | NODE_ENV=production |
| stayconnect-caddy | `caddy run --config /etc/caddy/Caddyfile` | caddy | Type=notify, CAP_NET_BIND_SERVICE |

Makefile targets install these: `ctrlapi-install`, `phase1-install` (scd+portald), `phase2-install` (tc-setup+acctd), `web-install`; plus `infra-up/down`, `migrate/migrate-down`, `psql`, builds, `fmt/vet/test`.

### Caddy (`deploy/caddy/`)

Global `local_certs` (internal CA — the pilot has no public DNS; swap for `email …` when a real domain exists). `trusted_proxies static 127.0.0.1`. h1/h2/h3. Shared security headers (HSTS, nosniff, DENY framing, referrer policy, no Server header).

| vhost | upstream | notes |
|---|---|---|
| portal.stayconnect.local | 127.0.0.1:8380 | no CSP (inline JS + OAuth); zstd/gzip; JSON log `/var/log/caddy/portal.log` |
| api.stayconnect.local | 127.0.0.1:8080 | strict CSP `default-src 'none'`; 30s response timeout for Stripe |
| admin.stayconnect.local | 127.0.0.1:3000 | Next-compatible CSP; `connect-src 'self' https://api.stayconnect.local` |

`Caddyfile.dev` variant: ports 9443/9080, admin API on 127.0.0.1:2020 (used by phase 14 tests). Going public later requires: `CTRLAPI_COOKIE_SECURE=true`, admin host in `CTRLAPI_ALLOW_ORIGINS`, updated Google OAuth redirect URI and Stripe webhook URL.

**Caddy operational gotchas (from the pilot):** `chown -R caddy:caddy /var/log/caddy` after first run (root-owned logs kill the service); disable the stock apt `caddy.service` (port-443 race with `stayconnect-caddy`); `/etc/caddy/Caddyfile` is a symlink to `/opt/stayconnect/deploy/caddy/Caddyfile`; on the VM `/etc/hosts` points the three hostnames at **10.10.0.1** (not 127.0.0.1) so network-namespace tests can reach them; Caddy's root CA is at `/var/lib/caddy/.local/share/caddy/pki/authorities/local/root.crt` (or use `curl -k`).

---

## 14. Observability (Prometheus / Grafana / Alertmanager)

`deploy/observability/docker-compose.yml`, all `network_mode: host`, loopback-bound:

| Service | Image | Port |
|---|---|---|
| prometheus | prom/prometheus:v2.55.0 | 127.0.0.1:9090 (30d retention) |
| alertmanager | prom/alertmanager:v0.27.0 | 127.0.0.1:9093 (config selectable via `ALERTMANAGER_CONFIG`) |
| grafana | grafana/grafana:11.3.0 | 127.0.0.1:3001 (3000 is taken by web-admin) |

**Scrape targets:** ctrlapi `127.0.0.1:8080/metrics`, scd `127.0.0.1:9101/metrics` (via `SCD_METRICS_ADDR`), Prometheus itself.

**Alert rules — 5 groups:**
- *system-health*: ScrapeTargetDown (crit), ApplianceOffline (flips counter, crit), ApplianceNoHeartbeats (crit)
- *ctrlapi-http*: CtrlapiHigh5xxRate >5% (crit), CtrlapiSlow p95 >2s (warn)
- *pms*: PMSProviderDown (crit), PMSValidationFailuresSpike (warn)
- *auth*: SocialLoginFailureRate >30% (warn), OTPVerifyLockoutsSpike >20/10m (warn)
- *payments*: StripeSignatureFailures (crit), **StripeVoucherIssueFailures (crit — guest charged without access)**, CheckoutCreateFailureRate >20% (warn)

**Alertmanager:** routes by severity — critical → `email-critical` ([CRIT], X-Priority 1, 1h repeat) with webhook fan-out (PagerDuty/Slack) **commented out pending real URLs**; warnings → `email-warning` (4h repeat); info → blackhole; critical inhibits same-alert warnings. Delivery is currently a **temporary Gmail relay** (`smtp.gmail.com:587` + app password — ⚠️ committed in the repo, see §20); plan is to swap to SendGrid. `alertmanager-dev.yml` stubs SMTP and points webhooks at a local Python sink (:9099) for the phase 15 tests.

**Grafana:** auto-provisioned Prometheus datasource + 4 dashboards in folder "StayConnect", all filterable by `tenant_id`:
- **Overview** — active sessions, appliances online, sessions started (by method) / closed (by reason), HTTP rates, nft set mutations, revenue
- **Payments** — 24h revenue, checkout success rate, webhook outcomes, revenue rate by currency
- **Auth** — OTP issued/verify outcomes, social logins + p95 latency, PMS validations + p95 latency
- **System Health** — scrape targets, uptimes, heartbeats/min, ctrlapi p95 by route, Go memory/goroutines, reaper closures, offline flips

Known gaps: no postgres/redis/nats exporters; single-node Prometheus + Alertmanager; Grafana not behind Caddy.

---

## 15. High availability (`deploy/ha/`, phase 5.5)

Active/passive appliance pair for one site:

- **keepalived** — VRRP instance `VI_GUEST` (vrid 51, master prio 150 / backup 100, 1s adverts, auth PASS) owning the guest gateway VIP (e.g. 10.10.0.1/24). Health-tracks scd via `curl --unix-socket /run/stayconnect/scd.sock /v1/health` (2s interval, fall 2 → detection ≈4s). Preemption on.
- **conntrackd** — FTFW sync of conntrack state over a dedicated sync interface (multicast 225.0.0.50:3780), so established flows survive failover. `stayconnect-ha-notify.sh` hooks keepalived transitions (master: commit+bulk-send; backup: resync+flush).
- **nft auth-set replication** — every scd `Allow`/`Deny` publishes `{op, ip, ttl, sender}` on NATS `nft.{siteID}`; the peer applies it locally (self-echo suppressed). Boot reconcile rebuilds `auth_ipv4` from active `sessions` rows so a promoted backup never starts empty.
- Shared state: sessions rows via shared Postgres; PMS config reload via `config.{tenantID}.pms` (queue group = one node reloads); liveness via `hb.{applianceID}`.
- Failover budget: ~3s on power loss (3 missed adverts) + ~200ms conntrackd resync; ~4s on scd crash.
- Known limits: no split-brain detection (sync-VLAN loss → dual master); NATS is a SPOF for nft sync (3-node cluster recommended).

---

## 16. Testing — the 25 phase E2E suites

`scripts/phase*-test.sh` — bash, `set -euo pipefail`, run **on the appliance VM**; runbook: `for s in phase*-test.sh; do bash $s; done`, expect `ALL GREEN` per script. **All 25 pass on the pilot VM as of 2026-04-21.**

Bootstrap scripts: `phase1-bootstrap.sh` (tenant/site/appliance seed, binaries install, TLS cert, `stayconnect` user), `phase1-test-client.sh` (network-namespace fake guest: veth into br-lan, dhclient, captive-probe, voucher auth, walled-garden checks), `phase3-bootstrap.sh` (ctrlapi env + platform admin `admin@stayconnect.local` + stub IdP row).

| Phase | Suite | Covers |
|---|---|---|
| 1 | test-client | captive DNAT, DHCP, voucher auth, walled garden (netns guest) |
| 2 | quota | template quotas: duration, kbps, device limit, expiry |
| 3 | api | ctrlapi admin API smoke (cookie login) |
| 4.1/4.2 | email/sms OTP | OTP issue/verify via stub logs; phone normalization |
| 4.3 | social | stubbed Google flow, state rows |
| 4.4 | sso | operator SSO via stub OIDC through the Next proxy |
| 4.5/4.5.5b | pms | Stub PMS E2E; field-map/normalization/stay-grace/test/cache |
| 5.1 | enrollment | keypair, bootstrap token, signed hello, replay + revoked rejection |
| 5.2 | nats | NATS RPC transport (test/cache/health/revoke) |
| 5.3 | reload | live PMS config reload without restart |
| 5.4 | heartbeat | online→offline→online staleness sweeps |
| 5.5 | ha | nft set replication + boot reconcile |
| 5.6 | overrides | per-site PMS overrides, partial unique indexes |
| 5.7 | polish | walled-garden /effective, reload safety loop, token sweeper, effective-config |
| 6 | session lifecycle | expires_at + reaper (expiry/idle) |
| 7 | metrics | Prometheus exposition, route labels |
| 8 | notifications | SendGrid/Twilio provider CRUD + loader + health |
| 9 | social | real Google OAuth provider CRUD + loader |
| 10 | mews | Mews REST provider |
| 11 | apaleo | Apaleo OAuth2 provider |
| 12 | payments | Stripe signature/webhook idempotency/checkout |
| 13 | observability | Prom targets, alert groups, dashboards |
| 14 | tls | Caddy dev proxy, headers, CSP |
| 15 | alertmanager | routing, inhibition, silences (local webhook sink) |

**Test-script gotchas** (all bit us during the 2026-04-21 regression sweep; none were code bugs):
1. `echo | grep -q` under `set -euo pipefail` is a SIGPIPE landmine (exit 141) — use here-strings `grep -q pat <<<"$var"`.
2. `ON CONFLICT` against the **partial** unique indexes from migration 0014 must name the predicate: `ON CONFLICT (tenant_id, name) WHERE site_id IS NULL`.
3. Hostnames pinned to 127.0.0.1 break `ip netns exec` tests (netns-local loopback) — point `portal/api/admin.stayconnect.local` at 10.10.0.1.
4. `journalctl --since '1h ago'` can miss boot-time logs — widen the window.
5. State contamination: earlier phases consume OTP/PMS rate-limit budget for the test IP — wipe `auth_otps`/`pms_attempts` at the start of phases 8/10.

---

## 17. Roles & user guides

`docs/user-guide/` is a task-oriented manual set:

- **platform-admin.md** — onboarding tenants (create tenant → seed first tenant_admin), tenant health checks, scoping via the Tenant selector (writes audited as you), offboarding (Disable = soft-delete), plan catalog via SQL, platform-wide monitoring via Grafana.
- **tenant-admin.md** — first-time checklist (password → sites → appliances → auth methods → walled garden → staff → test), appliance status semantics (Online <2 min / Stale 2–10 / Offline >10), full auth-method setup walkthroughs, operator management (disable-don't-delete), plan changes (upgrades immediate, downgrades next cycle).
- **tenant-operator.md** — daily loop (Dashboard → Sessions → Vouchers), voucher batch restock, abuse signals (shared codes >5 devices, 10× data, PMS guessing), quota resets, refunds.
- **viewer-and-billing.md** — read-only and plan-change-only roles.
- **common-tasks.md** — the triage tree for "guest can't log in" (association → portal pop → per-method debugging → post-auth issues), voucher/PMS/OTP/social troubleshooting (incl. stay-window and per-room lockout semantics), appliance-offline runbook, zero-sessions runbook.

Admin console glossary: **Tenant** (customer account) → **Site** (property) → **Appliance** (gateway) → **Session** (one connected device). **Voucher** = prepaid code; **Walled garden** = pre-auth reachable hosts; **PMS** = hotel reservation system.

---

## 18. Live pilot VM & dev workflow

- **VM:** `172.21.60.23` (hostname `radius`), SSH as root with ed25519 key auth (`ssh root@172.21.60.23`). Runs everything: ctrlapi, scd/portald/acctd, web-admin, Caddy, docker-compose infra + observability.
- **Code on VM:** `/opt/stayconnect/` — **neither the VM nor the Windows workspace is a git repo.** Sync with `scp 'd:\WebProjects\StayConnectEnterprise\<path>' root@172.21.60.23:/opt/stayconnect/<path>`, then rerun the relevant phase test.
- Beware the *other* radius host `172.21.96.200` — a separate legacy deployment; the real one is `.60.23`.
- Pilot conveniences: `SCD_PMS_STUB_SEED=true` in `/etc/stayconnect/scd.env` (seeded rooms 101/102/103 + future/past guests for stay-window tests), stub notification/social providers, dev admin `admin@stayconnect.local`.
- Hypervisor: VMware ESXi — remember the LAN portgroup security settings (§3).

---

## 19. Phase history / current status

Delivered phases (all E2E-green as of 2026-04-21):

- **1–2**: captive gateway core — nftables, DHCP/DNS, portald, voucher auth, tc shaping, acctd quotas.
- **3**: control-plane admin API.
- **4.x**: guest auth expansion — email OTP, SMS OTP, social login, operator SSO, PMS auth (+ PMS abstraction with field maps/normalization/stay windows).
- **5.x**: appliance identity & fleet — Ed25519 enrollment, NATS transport, live config reload, heartbeat/liveness, HA pair, per-site overrides, polish.
- **6**: session lifecycle (expires_at + reaper).
- **7**: Prometheus metrics.
- **8**: real notification providers (SendGrid/Twilio).
- **9**: real Google OAuth.
- **10/11**: Mews and Apaleo PMS providers.
- **12**: Stripe guest payments.
- **13**: observability stack (Prometheus/Grafana/Alertmanager).
- **14**: TLS edge via Caddy (internal CA).
- **15**: Alertmanager live email (Gmail relay).

Agreed next moves (from the 2026-04-20 review): swap Gmail → SendGrid for alerts; public TLS (ACME) when a domain/public IP exists; re-enable PagerDuty/Slack webhook fan-out; ops hardening — Postgres backup cron, docker log rotation, container resource limits, reboot drill, Grafana behind Caddy, Prometheus self-monitoring alert.

---

## 20. Known gaps, gotchas & security notes

**Security / secrets**
- ⚠️ `deploy/observability/alertmanager/alertmanager.yml` contains a **live Gmail address + Google app password committed in plaintext** (marked TEMPORARY). Rotate the app password and move to env substitution / SendGrid.
- nftables `input` still accepts ctrlapi :8080 and web-admin :3000 **from the WAN** (commented "restrict later") — lock down before any real deployment.
- Postgres/Redis/NATS credentials are dev defaults (`stayconnect`/`stayconnect`), loopback-bound.
- Appliance JWT replay cache is in-process — promote to Redis before running multiple ctrlapi replicas.
- Operator cookie needs `CTRLAPI_COOKIE_SECURE=true` once behind HTTPS in production.

**Architecture debt**
- Data plane reads/writes the **central Postgres directly**; the planned local-cache + NATS-sync split is TODO.
- `policyd` named in README but not implemented (by design so far).
- IPv4-only end to end (nft set, shaping, session handlers).
- Config push covers PMS only; walled-garden/ticket-template changes rely on appliance-side polling/effective-config fetches.
- Invoices/`usage_counters` tables have schema but no handlers; plan catalog has no admin UI (SQL only).
- Provider coverage: social = Google only (others stubbed); email = SendGrid (SES reserved); SMS = Twilio (MessageBird mentioned only); operator SSO = stub IdP only in this build.
- No RLS in Postgres — tenant isolation is app-enforced (`EffectiveTenantID` + per-query tenant_id filters).

**Operational gotchas (hard-won)**
- RFC 8910 / DHCP option 114 lives only in the **VM's live Kea config**, not the repo — re-add after any rebuild (§3).
- ESXi LAN portgroup: Promiscuous/MAC-changes/Forged-transmits must be **Accept** (§3).
- Caddy: chown `/var/log/caddy`, disable stock unit, `/etc/hosts` → 10.10.0.1 for netns tests (§13).
- `SCD_PMS_STUB_SEED` must survive `reloadPMS()` — the seed hook exists in both main() and reload; don't remove it (§6).
- Bash test scripts: SIGPIPE with `grep -q`, partial-index `ON CONFLICT`, netns loopback resolution (§16).
- DHCP option changes only reach clients on lease renewal — "Forget network" to test.
