# Security Hardening — Known Issues & Fixes

> The honest list. Each item states the risk, the fix, and the **current
> status** as of 2026-07-11. Items marked OPEN are release blockers for any
> non-pilot deployment.

## 1. Committed Gmail app password — OPEN, REQUIRES EXTERNAL ROTATION

`deploy/observability/alertmanager/alertmanager.yml` contains a **live Gmail
address and Google app password in plaintext**, committed to the tree (marked
TEMPORARY when phase 15 landed).

- **Status: the app password has NOT been rotated.** Removing it from the file
  is not sufficient — it exists in every checkout and backup already. Rotation
  must happen **in the Google account** (revoke the app password), which is an
  external manual action outside this repo.
- Fix sequence: (1) revoke the app password in the Google account; (2) replace
  SMTP delivery with SendGrid (already the agreed plan) or inject credentials
  via environment substitution / a secrets file excluded from the tree;
  (3) verify alert delivery end-to-end afterwards (phase 15 suite).

## 2. WAN-open ctrlapi :8080 and :3000 — OPEN (to be closed)

(The legacy `web-admin` console that listened on :3000 on the appliance has since been removed from the
tree; the Central console is `cloud-admin`, loopback `:3000` behind Caddy on the Central host.)

The nftables `input` chain still accepts TCP 8080 and 3000 **from the WAN
interface** (dev-era rule, commented "restrict later"). Verified live on the
pilot: ctrlapi listens on all interfaces.

- Fix: remove both accepts from `deploy/nftables/stayconnect.nft`; bind
  any remaining listener to loopback/mgmt and front it only via Caddy. In the
  target architecture the appliance runs no ctrlapi at all and Admin Console is
  served **only on the management interface** ([EDGE_ARCHITECTURE.md](EDGE_ARCHITECTURE.md) §5).

## 3. Dev database credentials — OPEN

Postgres/Redis use dev defaults (`stayconnect`/`stayconnect`),
loopback-bound. Acceptable only on the single-box pilot.

- Fix: per-service generated secrets; **separate credentials per database** —
  the cloud role must have no grants on `stayconnect_site` and the site role
  none on `stayconnect` (this credential split is part of the migration
  runbook, Phase 3, and is what makes the one-instance pilot topology
  acceptable). (Neither Central nor the appliance runs or connects to a message bus — CLAUDE.md §0E.)

## 4. IPv6 client bypass — OPEN (must drop v6 on client LAN)

The nftables table is `inet` family, so the **filter** chains do cover IPv6 —
but `auth_ipv4` is a v4 set and the captive **DNAT redirect is IPv4-only**.
Consequence: a client device using IPv6 (RA/SLAAC from anywhere, or a v6-capable
uplink) is never redirected to the portal, and v6 flows are never matched by
the auth set — an authentication bypass if v6 routing exists, and at minimum an
unshapen/unaccounted path.

- Interim fix (required now): **drop IPv6 entirely on the client LAN** — drop v6
  forwarding from the client bridge, drop RAs/DHCPv6 toward clients, and do not
  assign a v6 gateway. Filtering exists; the drop must be explicit policy.
- Real fix (Roadmap): dual-stack capture — `auth_ipv6` set, v6 DNAT/TPROXY,
  v6-aware shaping and session accounting.

## 5. Secure cookies — OPEN (config flag)

Operator session cookies need `CTRLAPI_COOKIE_SECURE=true` (and the edged
equivalent) once behind HTTPS — mandatory in production Caddy deployments;
currently defaults to off for the dev HTTP path.

## 6. Grafana exposure — OPEN

Grafana (127.0.0.1:3001) is not behind Caddy: no TLS, no central auth, and
port-forward habits on the pilot expose it wider than intended.

- Fix: publish via a Caddy vhost on the cloud (auth headers + TLS), keep the
  listener loopback-only; disable anonymous access; rotate the admin password.

## 7. Appliance-JWT replay cache is in-process — OPEN (scale gate)

`applianceauth.ReplayCache` (2-min window, 8192 entries) lives in ctrlapi
process memory. With a single replica that's sound; with horizontally scaled
ctrlapi, a JWT replayed against a *different* replica would pass.

- Fix: promote the jti replay cache to shared storage (Redis, `SETNX` with
  TTL = token lifetime) before running >1 ctrlapi replica. Not a pilot risk;
  a hard precondition for scaling.

## 8. Additional hardening (target architecture)

| Item | Status / note |
|---|---|
| Client-PII boundary | Enforced by design: client data exists only on the appliance, which has no telemetry subsystem (CLAUDE.md §0E; removed by appliance migration 0093); the guest-domain tables on Central were dropped (migration 0046) and the archived guest history with the `legacy_archive` schema (migration 0047) |
| License anti-rollback | Implemented: monotonic `license_version` + issued_at + revoked-id store + 48h clock high-water ([LICENSING_AND_ENTITLEMENTS.md](LICENSING_AND_ENTITLEMENTS.md) §7) |
| Vendor signing key | 0600 file, cloud-only; escrow + rotation procedure documented; treat as CA-grade secret ([BACKUP_AND_RESTORE.md](BACKUP_AND_RESTORE.md) §2) |
| Admin Console exposure | Mgmt interface only, never WAN or client network — enforce in Caddy binds *and* nftables input chain |
| Provider secrets (PMS/Stripe/Twilio/SendGrid/OAuth) | Write-only in APIs; stored per-site in the site DB; never sync |
| No RLS | Cloud tenant isolation remains app-enforced (`EffectiveTenantID`); the edge split removes the worst blast radius (client data), RLS on the cloud DB remains desirable — Roadmap |
| Registration / activation | Token-less, self-signed registration (proof of the identity key), rate-limited per client address; clone and hardware-reuse attempts are refused with a security alert; a retired identity key is recorded (`retired_appliance_identities`) and refused (`identity_retired`); nothing is authorized until a platform admin activates it (step-up) and the appliance verifies the signed assignment against its pinned key registry. Enrollment tokens no longer exist ([CENTRAL_CONTROL_PLANE.md §5](CENTRAL_CONTROL_PLANE.md#5-security-invariants-unchanged-by-this-redesign)) |
| Customer boundary on an appliance | A move never changes the customer (`409 cross_customer_move`); a new customer requires retire → factory-clean install → registration → activation, and an appliance Central deletes after it held a customer enters a persistent removed state and never re-registers, so no customer's local data can reach another customer's activation |
| Retirement | Two-phase and acknowledged for Retire and for hardware replacement: credentials are revoked only after the appliance's signed acknowledgement (retried until Central confirms) or on an emergency retire, so a retired box can always collect its retirement |
| Central operator sign-in | Email + password only (no SSO; migration 0047 dropped the SSO columns); login and re-authentication are rate-limited on the server-derived client address (`clientip`: the TCP peer, or `X-Real-IP` only from the loopback proxy); licence and activation writes, and every change to a sign-in (Team and customer users), need a recent password re-entry |
| Portal HTTP | Plain HTTP on the captive path is required for RFC 8910 probes; scope it to the client-network interface only |

## 9. Review checklist before pilot cutover

- [ ] Gmail app password revoked at Google (item 1) — **external action**
- [ ] WAN accepts for 8080/3000 removed; listeners rebound (item 2)
- [ ] Per-DB credentials in place; cross-grants verified absent (item 3)
- [ ] IPv6 dropped on client LAN; verified with a v6-configured client (item 4)
- [ ] `COOKIE_SECURE` on for both ctrlapi and edged behind Caddy (item 5)
- [ ] Grafana behind Caddy or firewalled (item 6)
- [ ] Single ctrlapi replica confirmed, or replay cache in Redis (item 7)
- [ ] Offline drill + reboot drill green ([OFFLINE_OPERATION.md](OFFLINE_OPERATION.md))
