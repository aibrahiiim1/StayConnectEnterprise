# Offline Operation

> The defining property of the edge-first architecture: **a hotel's guest WiFi
> and its Hotel Admin keep working with the cloud and even the whole internet
> uplink's control traffic down.** This doc states exactly what
> continues, what pauses, and how to prove it.

## 1. What continues (cloud/internet down)

Everything that reads/writes only the site-local DB and local kernel state:

| Function | Why it survives |
|---|---|
| Captive portal, DHCP, DNS | portald/Kea/Unbound are local; nftables DNAT is kernel-local |
| Voucher login | `vouchers`/`ticket_templates` are local tables |
| PMS login (FIAS: Protel/Opera/Fidelio) | FIAS is a **local TCP link to the hotel's own PMS** — no internet involved; the in-memory room cache keeps serving |
| Email OTP *issue against local rules* — see pauses | OTP rows are local; delivery needs the provider (below) |
| Session concurrency/limits | `tenant_effective_limits` is a local table fed by the signed license |
| Bandwidth shaping, data/time quotas, accounting | tc + acctd + local `accounting_records` |
| Session reaper, idle timeout, natural expiry | scd local loops |
| Hotel Admin (all of `/edge/v1`) | edged + hotel-admin are served from the appliance's **WAN/management IP** (two-NIC rule: WAN is the management interface) |
| Local operator login | argon2id against local `operators` |
| Voucher batch creation, GuestAccessPlan edits, walled garden, branding | local writes (license state permitting) |
| License enforcement | evaluated offline from the persisted signed document — the same gate and the same concurrent-guest capacity for every method, PMS room sign-in included |
| HA failover | **NOT available** — HA failover under the final two-NIC (WAN+LAN) architecture is **not yet designed, implemented, or accepted** (the old third-NIC `hasync` design is superseded; the HA-sync transport is an OPEN decision). **Single-appliance local-first/offline operation is what is current and supported.** The VRRP/conntrackd/nft/DB-replication ideas are design intent only. |
| Backups | local pg_dump + `backup_records` |

## 2. What pauses or degrades

| Function | Behavior offline |
|---|---|
| License refresh | current document keeps governing; Hotel Admin → **Appliance & licence** shows OneGate Central *Temporarily unreachable*; `CloudStale` warning after `offline_grace_days` without cloud validation — never degrades guest function while the document is valid |
| Registration / activation | a not-yet-registered appliance keeps retrying (30 s backing off to 5 min); a site without internet uses offline activation |
| Telemetry | none — the appliance has no telemetry subsystem (CLAUDE.md §0E); nothing queues for Central |
| Retirement acknowledgement, offline-package reconciliation | kept on the appliance and retried until Central confirms; neither is on a guest path |
| Central visibility | the console shows the appliance's connection as *Recently seen*, then *Offline* after 24 h; nothing else changes |
| **External providers** (inherently internet-dependent) | Twilio SMS OTP and SendGrid email OTP cannot deliver → those portal tabs fail; Google social login fails; **Stripe paid WiFi** checkout fails. Voucher and FIAS-PMS login remain as the offline-safe methods |
| Mews/Apaleo (cloud-hosted PMS) | unreachable → PMS logins via those providers fail upstream (`upstream_fail`); FIAS is unaffected |

Design rule: only *registration, assignment/certificate/licence refresh and
external providers* are allowed to depend on connectivity. Anything else
touching the network at runtime is a bug. (Central offers no remote control of
an appliance, so there is nothing else to lose.)

## 3. The grace model, worked example

License (schema 3, as Central issues it): `valid_until = 2027-07-11`,
`grace_period_days = 30`, `offline_grace_days = 30`.

| Date | Cloud reachable? | State | Guest impact |
|---|---|---|---|
| 2026-12-01 | last validation 2026-11-30 | Active | none |
| 2027-01-15 | offline since 2026-12-10 (36d) | Active, **CloudStale** warning | none — document still valid |
| 2027-07-12 | still offline | **GracePeriod** (until 2027-08-10) | none; renewal banner in Hotel Admin |
| 2027-08-11 | still offline | **Expired** | new sessions refused (service notice); running sessions finish; Hotel Admin keeps working, licence upload available |
| any time | operator uploads a renewed licence file in Hotel Admin (**Appliance & licence → Upload licence file**) | per new document | instant recovery — no cloud needed even for renewal |

Legacy (schema 1/2) documents insert a **Restricted** window between grace and
expiry ([LICENSING_AND_ENTITLEMENTS.md](LICENSING_AND_ENTITLEMENTS.md) §4).

Clock tricks don't extend this: evaluation uses `max(now, high-water)` when the
clock is rolled back >48h ([LICENSING_AND_ENTITLEMENTS.md](LICENSING_AND_ENTITLEMENTS.md) §7).

## 4. Cloud-outage test procedure

Run on a non-production appliance. Expected total time ≈ 20 minutes.

1. **Baseline**: `bash scripts/phase1-test-client.sh` green; Hotel Admin →
   **Appliance & licence** shows OneGate Central *Connected*.
2. **Sever the cloud**: drop it at the appliance's firewall —
   `nft add rule inet filter output ip daddr <central-ip> drop`.
3. **Guest path must stay green**:
   - new voucher login from the netns client succeeds;
   - PMS (stub/FIAS) login succeeds;
   - existing session keeps passing traffic; shaping/quota still enforced;
   - `curl --unix-socket /run/stayconnect/scd.sock http://localhost/v1/health` ok.
4. **Hotel Admin must stay green**: log in at `https://<mgmt-ip>`, create a
   voucher batch, disconnect a session, view reports.
5. **Expected degradations only**: SMS/social tabs fail with provider errors
   (if the uplink was cut too); scd logs license-fetch retries — no crash
   loops; Hotel Admin shows OneGate Central *Temporarily unreachable* with the
   last problem.
6. **License evaluation unchanged**: `GET /edge/v1/central/status` still
   reports `license.state` `active` (CloudStale only if the outage exceeds
   grace days — don't expect it in a short drill).
7. **Restore the cloud**; press **Check now** and verify Hotel Admin shows
   *Connected* and Central's **Appliances** page shows the appliance
   *Connected* again.
8. **Re-run** the relevant phase suites (1, 2, 4.5) to confirm no regression.

Failure of step 3 or 4 is a release blocker: it means a guest-path component
still has a hidden cloud dependency (compare
[CURRENT_STATE_ASSESSMENT.md](CURRENT_STATE_ASSESSMENT.md) §3 — the exact
defect this refactor removes).

## 5. Reboot-while-offline

A power-cycled appliance with no cloud must come back serving guests:
identity, license (`current.json` + `state.json`), and all guest data are on
local disk; nft `auth_ipv4` is rebuilt from active `sessions` rows at boot
(HA boot-reconcile path). Include one reboot in every offline drill.
