# Data Ownership Matrix

> Which database owns each table, what crosses the boundary, and the PII rule.
> Cloud DB = `stayconnect` (central Postgres). Edge DB = `stayconnect_site`
> (one isolated database per hotel site).

## 1. The rule

**The cloud owns the commercial relationship; the hotel owns the guests.**
Guest PII is created, used and retained exclusively at the site. What flows up
is aggregated operational telemetry; what flows down is signed entitlements and
config-change signals. Neither direction ever carries a guest identity.

## 2. Cloud-owned tables

| Table | Notes |
|---|---|
| `tenants` | customer accounts (commercial identity) |
| `sites` | property registry |
| `appliances` | inventory, Ed25519 public keys, status/liveness, version |
| `appliance_bootstrap_tokens` | enrollment (hashed, single-use) |
| `plans`, `plan_limits`, view `commercial_plans` | CommercialPlan catalog |
| `tenant_subscriptions`, `tenant_limit_overrides`, `subscription_events` | subscription state |
| `tenant_effective_limits` (VIEW) | merged plan+override limits — input to license issuance |
| `invoices`, `invoice_lines` | schema only (Roadmap — billing automation not yet implemented) |
| `licenses` | signed envelopes + projections; one current per site |
| `fleet_telemetry`, `fleet_telemetry_dedupe` | non-PII telemetry + exactly-once gate |
| `operators`, `operator_roles`, `idp_providers`, `auth_oidc_states` | **platform/group** operators only |
| `audit_log` | platform/group actions |

## 3. Edge-owned tables (never leave the hotel)

| Table | PII? | Notes |
|---|---|---|
| `tenants` (1 row) | — | this site's identity + `auth_methods` + `branding` |
| `sites` (1 row), `appliances` | — | local mirror of this site's identity |
| `operators`, `operator_roles` | staff emails | the seven site roles; hotel staff accounts live here, not in the cloud |
| `ticket_templates` (**GuestAccessPlan**) | — | |
| `voucher_batches`, `vouchers` | codes | voucher codes are treated as secrets (PII-adjacent) |
| `guests` | **YES** | MAC, name, email, phone, consent |
| `sessions` | **YES** | IP, MAC, per-guest timing |
| `accounting_records` | **YES** | per-session byte counters |
| `auth_otps` | **YES** | hashed codes, destinations (email/phone) |
| `social_oauth_states` | **YES** | IP+MAC-bound OAuth state |
| `pms_providers` | credentials | PMS hosts/keys — hotel infrastructure secrets |
| `pms_attempts` | **YES** | room numbers, attempt IPs |
| `walled_garden_rules` | — | |
| `notification_providers`, `social_oauth_providers`, `stripe_accounts` | credentials | provider secrets stay on-site |
| `payments`, `stripe_events` | **YES** | client IP/MAC, Stripe references |
| `audit_log` | staff + guest refs | local compliance record |
| `tenant_effective_limits` (plain TABLE) | — | derived from the signed license; local bridge |
| `sync_outbox`, `sync_checkpoints` | — by contract | outbox payloads must be aggregates only |
| `backup_records` | — | |

## 4. What syncs (and what never does)

### Edge → Cloud (via `sync_outbox` → NATS `telemetry.<applianceID>`)

Allowed kinds only: `heartbeat`, `health`, `usage`, `auth_counts`,
`pms_health`, `license_ack`, `backup`, `sync`, `update_progress`.

| Kind | Example payload content |
|---|---|
| heartbeat | version, uptime |
| health | daemon states, DB ok, disk, load |
| usage | aggregate session counts, total bytes per period |
| auth_counts | logins per method, failure counts (counts, not identities) |
| pms_health | provider status/last_record_at (no reservation data) |
| license_ack | installed license_id, evaluated state |
| backup | last backup status/size |
| sync | outbox depth, drain lag |
| update_progress | Roadmap — update orchestration not yet implemented |

**Defense in depth:** even though payloads are aggregates by contract, the cloud
ingest (`fleet.Sanitize`) strips any key case-insensitively containing
`mac`, `email`, `phone`, `guest_name`, `first_name`, `last_name`, `room`,
`reservation`, `voucher_code`, `code`, `otp`, `password`, `ip` — at the top
level and one nesting level — before storage.

### Cloud → Edge

| Item | Channel |
|---|---|
| Signed license envelope + revoked license IDs + `server_time` | pull: `GET /v1/appliance/license` (appliance Ed25519 JWT); a push notice over NATS may prompt an immediate fetch |
| Config-change events | NATS `config.<tenantID>.pms` (today: PMS reload signal — the data itself is already local) |
| Revocation notices | embedded in the license fetch response (`revoked[]`) |

### Never syncs, in either direction

- Guest identities, MACs, IPs, emails, phones, names, room numbers,
  reservation data, OTP codes, voucher codes, session rows, accounting rows.
- Local operator password hashes.
- PMS / Stripe / SendGrid / Twilio / OAuth credentials.
- The vendor **private** signing key (cloud-only, never leaves).

## 5. The PII boundary, precisely

```
        HOTEL SITE (edge DB)                 │            CLOUD
  guests · sessions · accounting · OTP       │   tenants · sites · appliances
  PMS attempts · payments · vouchers         │   plans · subscriptions · licenses
  local operators · local audit              │   fleet_telemetry (aggregates)
                                             │   platform operators · audit
        ──────── sync_outbox ────────────────▶   Sanitize() → fleet_telemetry
              (aggregate kinds only)         │
        ◀──────── signed license ────────────    (no guest data ever crosses)
```

Consequences:

- A cloud compromise cannot expose hotel guests — the data simply is not there.
- GDPR/data-locality: guest data residency equals the hotel's own premises;
  retention is enforced locally from the license's
  `accounting_retention_days` / `audit_retention_days` limits.
- Cloud support staff diagnose sites from telemetry and, when needed, ask hotel
  staff to act in Hotel Admin. (Remote support sessions: Roadmap — not yet
  implemented.)

## 6. Duplicated-by-design rows

The edge DB's `tenants`/`sites`/`appliances` rows mirror the cloud's registry
for this one site (same UUIDs, copied by `sitemigrate` at cutover, thereafter
maintained locally). This duplication is intentional: FK integrity for the
guest domain without any runtime cloud dependency. Commercial fields (status,
subscription) are **not** authoritative locally — entitlement truth is the
signed license.

## 7. Compatibility window

Until the pilot cutover completes, the central DB still contains the historical
guest-domain rows. After `sitemigrate` copies a site and scd/acctd re-point to
the site DB, the central copies are frozen (read-only legacy views for the
deprecated `/v1` adapters) and are dropped when the adapters are removed
([API_DEPRECATIONS.md](API_DEPRECATIONS.md), [MIGRATION_RUNBOOK.md](MIGRATION_RUNBOOK.md)).
