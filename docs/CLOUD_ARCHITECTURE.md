# Cloud Architecture

> The vendor/commercial half of the edge-first split. The cloud manages
> customers, inventory, entitlements and fleet health. It never serves a guest
> and never stores guest PII. Edge counterpart: [EDGE_ARCHITECTURE.md](EDGE_ARCHITECTURE.md).

## 1. Services

| Service | Binary / stack | Role |
|---|---|---|
| `ctrlapi` | Go (`control-plane/`), `:8080` behind Caddy | Cloud API: `/cloud/v1/*` (operator-facing) + appliance protocol endpoints (`/v1/appliances/enroll`, `/v1/appliance/hello`, `/v1/appliance/license`) + legacy deprecated `/v1/*` adapters |
| `cloud-admin` | Next.js UI, served centrally | Platform + group operators: tenants, sites, appliances, CommercialPlans, subscriptions, licenses, fleet |
| Postgres + TimescaleDB | `stayconnect` DB | Cloud-owned tables only (see §2) |
| Redis | operator session store | Opaque `sc_session` cookies, 12h sliding TTL |
| NATS (JetStream) | cluster in production | Inbound: `telemetry.<applianceID>`, `hb.<applianceID>`; outbound: `config.<tenantID>.pms` config pushes, `scd.<applianceID>.>` RPC |
| Observability | Prometheus / Grafana / Alertmanager | `ctrlapi_*` metrics, fleet alerting |
| Vendor signing key | `CTRLAPI_VENDOR_KEY` (64-byte Ed25519 private key file, 0600) | Signs license documents. Exists **only** in the cloud; appliances hold public keys only. When unset, `Deps.Licensing` is nil and license routes are not mounted. |

## 2. Cloud database ownership

The cloud DB keeps exactly the commercial + fleet domain:

| Table(s) | Contents |
|---|---|
| `tenants` | customer accounts (hotel groups) — commercial identity, not portal config |
| `sites` | properties per tenant |
| `appliances`, `appliance_bootstrap_tokens` | gateway inventory, Ed25519 public keys, enrollment tokens, liveness |
| `plans` + `plan_limits` (+ read-only view **`commercial_plans`**, migration 0019) | CommercialPlan catalog — what StayConnect sells |
| `tenant_subscriptions`, `tenant_limit_overrides`, `tenant_effective_limits` (view), `subscription_events`, `invoices`/`invoice_lines` | subscription state; invoices remain schema-only (Roadmap — billing automation not yet implemented) |
| `licenses` | signed entitlement envelopes + queryable projections; **exactly one current (`active`/`suspended`) license per site** enforced by partial unique index |
| `fleet_telemetry` (hypertable, 7-day chunks) + `fleet_telemetry_dedupe` | aggregated non-PII appliance telemetry + exactly-once gate |
| `operators`, `operator_roles`, `idp_providers`, `auth_oidc_states` | **platform and group** operators only — site-level operators live in each site DB |
| `audit_log` (hypertable) | platform/group actions (license.issued, tenant.created, …) |

Guest-domain tables (guests, sessions, vouchers, ticket_templates, PMS,
payments, OTP, …) are **edge-owned**; during the compatibility window they still
exist centrally but are frozen after `sitemigrate` cutover. Full matrix:
[DATA_OWNERSHIP.md](DATA_OWNERSHIP.md).

## 3. `/cloud/v1` API surface

All routes require an operator session (`RequireAuth`); role gates per handler.
Conventions unchanged from ctrlapi: error envelope `{error, message, trace_id}`,
list envelope `{data, meta}`.

| Route | Methods | Access | Notes |
|---|---|---|---|
| `/cloud/v1/version` | GET | public | service/version |
| `/cloud/v1/tenants[/{id}]` | GET/POST/PUT/DELETE | writes platform_admin | DELETE = soft archive; subscription, effective-limits, audit, usage sub-routes |
| `/cloud/v1/sites[/{id}]` | CRUD | session + tenant scope | `max_sites` enforced |
| `/cloud/v1/appliances[/{id}]` | CRUD | session + tenant scope | inventory + effective-config |
| `/cloud/v1/commercial-plans[/{id}]` | GET | session | unambiguous mount of the plans catalog (read-only) |
| `/cloud/v1/operators[/{id}]` | CRUD + set-password/roles | tenant_admin / platform_admin | platform + group operators |
| `/cloud/v1/appliance-bootstrap-tokens[/{id}]` | GET/POST/DELETE | session + tenant scope | single-use enrollment tokens |
| `/cloud/v1/fleet/` | GET | session (tenant-scoped; platform_admin may go unscoped) | appliance registry joined with current license status/validity + latest `health` telemetry payload |
| `/cloud/v1/fleet/{applianceID}/telemetry` | GET | session, tenant-checked | raw telemetry rows, `?kind=` filter, `?limit=` ≤1000 (default 100) |
| `/cloud/v1/licenses/` | GET | session (tenant-scoped) | list, `?site_id=` filter, newest first |
| `/cloud/v1/licenses/{id}` | GET | session (own tenant, or platform_admin) | projection incl. features/limits/key_id |
| `/cloud/v1/licenses/` | POST | **platform_admin** | issue: `{tenant_id, site_id, valid_days (default 365), offline_grace_days (default 30)}` → `201 {license_id, document, envelope}` |
| `/cloud/v1/licenses/{id}/revoke` | POST | **platform_admin** | flips status to `revoked`; delivered to the edge on its next license fetch |

Appliance-facing (Ed25519 appliance JWT, stays under `/v1`):

| Route | Purpose |
|---|---|
| `POST /v1/appliances/enroll` | bootstrap-token enrollment (public, token-gated) |
| `GET /v1/appliance/hello` | signed-identity smoke check |
| `GET /v1/appliance/license` | returns `{license_id, envelope, revoked[], server_time}`; a successful fetch counts as the edge's cloud validation |

## 4. Fleet telemetry ingest (`internal/fleet`)

Consumer subscribes to `telemetry.>` on NATS. Per message:

1. Decode `{appliance_id, seq, kind, ts, payload}`. **Subject identity wins**:
   `appliance_id` must equal the `telemetry.<applianceID>` subject suffix — an
   appliance can only speak for itself.
2. Validate `kind` ∈ {heartbeat, health, usage, auth_counts, pms_health,
   license_ack, backup, sync, update_progress} and `seq > 0`.
3. Resolve tenant/site from the `appliances` registry (never trusted from payload).
4. **Dedupe gate first**: `INSERT INTO fleet_telemetry_dedupe (appliance_id, seq)
   ON CONFLICT DO NOTHING`. Zero rows affected ⇒ replay ⇒ ack `200` without a
   second telemetry row (exactly-once landing).
5. Clock-skew guard: a missing or >24h-future `ts` is replaced with `now()`.
6. `fleet.Sanitize` strips any payload key (top level + one nesting level)
   case-insensitively containing: `mac, email, phone, guest_name, first_name,
   last_name, room, reservation, voucher_code, code, otp, password, ip`.
   Telemetry is aggregates-only by contract; this is defense in depth.
7. Insert into `fleet_telemetry`. On insert failure the dedupe row is deleted so
   the appliance's retry can land. Reply header `Nats-Status`: 200/400/404/500;
   the edge treats only 200 as sent. See [SYNC_PROTOCOL.md](SYNC_PROTOCOL.md).

## 5. Licensing issuance flow (`internal/licensing`)

```
platform_admin ──POST /cloud/v1/licenses──▶ ctrlapi
   │ 1. active subscription? (trialing/active/past_due) → plan code; else 402
   │ 2. site belongs to tenant? (cross-tenant safety)
   │ 3. collect non-retired appliance IDs for the site
   │ 4. project tenant_effective_limits →
   │      Features: feature.pms_integration, feature.paid_wifi,
   │                feature.auth.{sms_otp,email_otp,social}, feature.ha_pair,
   │                feature.white_label
   │      Limits:   max_appliances, max_concurrent_devices, max_operators,
   │                max_guest_access_plans, retention_days_{accounting,audit}
   │                (-1 / missing → 0 = unlimited in doc semantics)
   │ 5. build Document{license_id, tenant, site, appliances, plan code,
   │      status=active, issued_at=now, valid_until=now+valid_days,
   │      offline_grace_days, schema_version=1}
   │ 6. Signer.Sign → Envelope{payload_b64, signature, key_id}
   │ 7. tx: supersede previous current license for the site; insert new row
   │      (projection columns + exact signed_envelope text)
   ▼
audit_log: license.issued          appliance pulls it via GET /v1/appliance/license
```

Revocation: `POST /cloud/v1/licenses/{id}/revoke` sets `status='revoked',
revoked_at=now()`. There is no push channel requirement — the appliance's next
license fetch returns the site's revoked license IDs alongside the current
envelope, and the edge persists them in its local revocation store. Details and
edge-side state machine: [LICENSING_AND_ENTITLEMENTS.md](LICENSING_AND_ENTITLEMENTS.md).

## 6. What the cloud must never do

- Serve a captive portal or authorize a guest session.
- Store or receive guest PII (enforced by outbox content contract + `Sanitize`).
- Open a connection *to* an appliance — all links are appliance-initiated
  (NATS client connection outbound from the site; HTTPS license fetch outbound).
- Be a runtime dependency of the guest path: an appliance with a valid signed
  license operates fully with the cloud unreachable ([OFFLINE_OPERATION.md](OFFLINE_OPERATION.md)).
