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

## 2. WAN-open ctrlapi :8080 and web-admin :3000 — OPEN (to be closed)

The nftables `input` chain still accepts TCP 8080 and 3000 **from the WAN
interface** (dev-era rule, commented "restrict later"). Verified live on the
pilot: ctrlapi listens on all interfaces.

- Fix: remove both accepts from `deploy/nftables/stayconnect.nft`; bind
  ctrlapi/web-admin to loopback/mgmt and front them only via Caddy. In the
  target architecture the appliance runs no ctrlapi at all and Hotel Admin is
  served **only on the management interface** ([EDGE_ARCHITECTURE.md](EDGE_ARCHITECTURE.md) §5).

## 3. Dev database credentials — OPEN

Postgres/Redis/NATS use dev defaults (`stayconnect`/`stayconnect`),
loopback-bound. Acceptable only on the single-box pilot.

- Fix: per-service generated secrets; **separate credentials per database** —
  the cloud role must have no grants on `stayconnect_site` and the site role
  none on `stayconnect` (this credential split is part of the migration
  runbook, Phase 3, and is what makes the one-instance pilot topology
  acceptable). NATS gets per-appliance credentials scoped to its own subjects
  (`telemetry.<id>`, `hb.<id>`, `scd.<id>.>`).

## 4. IPv6 guest bypass — OPEN (must drop v6 on guest LAN)

The nftables table is `inet` family, so the **filter** chains do cover IPv6 —
but `auth_ipv4` is a v4 set and the captive **DNAT redirect is IPv4-only**.
Consequence: a guest device using IPv6 (RA/SLAAC from anywhere, or a v6-capable
uplink) is never redirected to the portal, and v6 flows are never matched by
the auth set — an authentication bypass if v6 routing exists, and at minimum an
unshapen/unaccounted path.

- Interim fix (required now): **drop IPv6 entirely on the guest LAN** — drop v6
  forwarding from the guest bridge, drop RAs/DHCPv6 toward guests, and do not
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
| Guest-PII boundary | Enforced by design (edge-only data) + `fleet.Sanitize` defense in depth — keep the key list in sync with any new telemetry kinds |
| License anti-rollback | Implemented: issued_at monotonicity + 48h clock high-water ([LICENSING_AND_ENTITLEMENTS.md](LICENSING_AND_ENTITLEMENTS.md) §7) |
| Vendor signing key | 0600 file, cloud-only; escrow + rotation procedure documented; treat as CA-grade secret ([BACKUP_AND_RESTORE.md](BACKUP_AND_RESTORE.md) §2) |
| Hotel Admin exposure | Mgmt interface only, never WAN or guest network — enforce in Caddy binds *and* nftables input chain |
| Provider secrets (PMS/Stripe/Twilio/SendGrid/OAuth) | Write-only in APIs; stored per-site in the site DB; never sync |
| No RLS | Cloud tenant isolation remains app-enforced (`EffectiveTenantID`); the edge split removes the worst blast radius (guest data), RLS on the cloud DB remains desirable — Roadmap |
| Enrollment | Single-use hashed bootstrap tokens, ≤7-day TTL, optional serial lock; opaque failures — unchanged, sound |
| Portal HTTP | Plain HTTP on the captive path is required for RFC 8910 probes; scope it to the guest interface only |

## 9. Review checklist before pilot cutover

- [ ] Gmail app password revoked at Google (item 1) — **external action**
- [ ] WAN accepts for 8080/3000 removed; listeners rebound (item 2)
- [ ] Per-DB credentials in place; cross-grants verified absent (item 3)
- [ ] IPv6 dropped on guest LAN; verified with a v6-configured client (item 4)
- [ ] `COOKIE_SECURE` on for both ctrlapi and edged behind Caddy (item 5)
- [ ] Grafana behind Caddy or firewalled (item 6)
- [ ] Single ctrlapi replica confirmed, or replay cache in Redis (item 7)
- [ ] Offline drill + reboot drill green ([OFFLINE_OPERATION.md](OFFLINE_OPERATION.md))
