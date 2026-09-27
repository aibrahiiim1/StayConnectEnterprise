# Data Ownership Matrix

> Which database owns each table, what crosses the boundary, and the PII rule.
> Cloud DB = `stayconnect` (central Postgres). Edge DB = `stayconnect_site`
> (one isolated database per hotel site).

## 1. The rule

**The cloud owns the licensing relationship; the hotel owns the guests.**
Guest PII is created, used and retained exclusively at the site. Central serves
the appliance for licensing only (CLAUDE.md §0E): what flows up is the
appliance's own identity, certificate request and acknowledgements; what flows
down is the signed assignment, certificate and signed licence. Neither
direction ever carries a guest identity, and no operational telemetry is sent.

## 2. Cloud-owned tables

Customers (`tenants`), `sites`, `appliances` and their assignment, PKI,
retirement, security-alert and offline-activation tables, `licenses` (one
current per appliance), `operators`/`operator_roles` (Central sign-ins only) and
`audit_log`. The full list is [CLOUD_ARCHITECTURE.md §2](CLOUD_ARCHITECTURE.md#2-central-database-ownership).

Retired commercial history (`plans`, `plan_limits`, `plan_limit_history`,
`subscription_events`) sits in schema `legacy_archive` and is read by nothing.
Subscriptions, plan views, enrollment tokens, SSO and fleet telemetry tables no
longer exist (migrations 0045 and 0046).

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

### Edge → Cloud (HTTPS, appliance-initiated)

| Item | Endpoint |
|---|---|
| Self-signed registration (serial, hardware fingerprint, identity public key, MACs) | `POST /v1/appliances/register` |
| Certificate signing request | `POST /v1/appliance/csr` |
| Signed hello (orphan detection) | `GET /v1/appliance/hello` |
| Assignment acknowledgement (adopted version) | `POST /v1/appliance/assignment/ack` |
| Offline-activation reconciliation | `POST /v1/appliance/offline-reconcile` |

**No telemetry.** The telemetry outbox and every producer (usage, health,
service-health, `license_ack`) are off, and the NATS transport is not opened
(CLAUDE.md §0E). The outbox tables remain on the appliance; retention still
prunes records delivered before the switch-off.

### Cloud → Edge (pulled by the appliance)

| Item | Endpoint |
|---|---|
| Signed assignment (customer, site, state, version) + signed key registry | `GET /v1/appliance/assignment`, `GET /v1/appliance/assignment-registry` |
| Client certificate | `GET /v1/appliance/certificate` |
| Signed license envelope + revoked license IDs + `server_time` | `GET /v1/appliance/license` |

### Never syncs, in either direction

- Guest identities, MACs, IPs, emails, phones, names, room numbers,
  reservation data, OTP codes, voucher codes, session rows, accounting rows.
- Local operator password hashes.
- PMS / Stripe / SendGrid / Twilio / OAuth credentials.
- The vendor **private** signing key (cloud-only, never leaves).

## 5. The PII boundary, precisely

```
        HOTEL SITE (edge DB)                 │            CLOUD
  guests · sessions · accounting · OTP       │   customers · sites · appliances
  PMS attempts · payments · vouchers         │   assignments · certificates
  local operators · local audit              │   licenses · Central operators · audit
                                             │
        ──── registration · CSR · ack ───────▶   (appliance identity only)
        ◀── signed assignment · cert · license    (no guest data ever crosses)
```

Consequences:

- A cloud compromise cannot expose hotel guests — the data simply is not there.
- GDPR/data-locality: guest data residency equals the hotel's own premises;
  retention is enforced locally on the appliance.
- Cloud support staff see only activation, connection and licence state; to
  diagnose a site they ask hotel staff to act in Hotel Admin.

## 6. Duplicated-by-design rows

The edge DB's `tenants`/`sites`/`appliances` rows mirror Central's registry
for this one site (same UUIDs). This duplication is intentional: FK integrity
for the guest domain without any runtime cloud dependency. Tenant and site are
**not** authoritative locally — the only authority is the verified signed
assignment, and entitlement truth is the signed license.

## 7. Central guest-domain tables are gone

The historical guest-domain tables on Central (guests, sessions, vouchers,
accounting, OTP, PMS, payments, walled garden, networks, …) were verified empty
and dropped by migration 0046, together with the deprecated `/v1` adapters that
read them ([API_DEPRECATIONS.md](API_DEPRECATIONS.md)).
