# API Deprecations — Legacy `/v1` Route Disposition

> The pre-refactor ctrlapi exposed everything under one `/v1/*` namespace.
> Canonical homes are now `/cloud/v1/*` (ctrlapi) and `/edge/v1/*` (edged, per
> appliance). Legacy guest-domain routes on ctrlapi remain temporarily as
> **deprecated compatibility adapters** (they keep the 25 E2E suites and the
> old web-admin working during the pilot) and are **removed after the pilot
> cutover**. See the mount comments in
> `control-plane/internal/http/router.go`.

## 1. Disposition legend

- **→ /cloud/v1** — same handler, re-mounted (sometimes renamed) in the cloud namespace.
- **→ /edge/v1** — resource is site-owned; the successor lives on the appliance (edged) against the site DB.
- **STAYS** — not deprecated; part of the appliance protocol or infra surface that remains on ctrlapi `/v1`.

## 2. Route table

| Legacy route (ctrlapi `/v1`) | Disposition | Successor | Notes |
|---|---|---|---|
| `GET /healthz`, `/readyz`, `/metrics` | STAYS | — | infra, outside `/v1` |
| `GET /v1/version` | STAYS (also mounted at `/cloud/v1/version`) | | |
| `POST /v1/auth/login`, `POST /v1/auth/logout`, `GET /v1/auth/whoami` | STAYS for cloud operators | planned `/cloud/v1/auth/*` mounts | edged has its **own** `/edge/v1/auth/*` for site operators |
| `GET/POST /v1/auth/sso/*` | STAYS (cloud operator SSO) | planned `/cloud/v1/auth/sso` | |
| `POST /v1/appliances/enroll` | **STAYS** | — | appliance protocol (bootstrap-token gated) |
| `GET /v1/appliance/hello` | **STAYS** | — | appliance protocol (Ed25519 JWT) |
| `GET /v1/appliance/license` | **STAYS** | — | license fetch — new in the refactor, appliance protocol |
| `/v1/tenants[/{id}]` + subscription/effective-limits/audit/usage | → /cloud/v1 | `/cloud/v1/tenants...` | already mounted |
| `/v1/sites[/{id}]` | → /cloud/v1 | `/cloud/v1/sites` | already mounted |
| `/v1/appliances[/{id}]`, effective-config | → /cloud/v1 | `/cloud/v1/appliances` | already mounted |
| `/v1/appliance-bootstrap-tokens[/{id}]` | → /cloud/v1 | `/cloud/v1/appliance-bootstrap-tokens` | already mounted |
| `/v1/plans[/{id}]` | → /cloud/v1 **renamed** | `/cloud/v1/commercial-plans` | naming rule: CommercialPlan vs GuestAccessPlan |
| `/v1/operators[/{id}]` + set-password/roles | **split** | `/cloud/v1/operators` (platform/group) **and** `/edge/v1/operators` (site staff) | one legacy surface becomes two account systems |
| `/v1/ticket-templates[/{id}]` | → /edge/v1 **renamed** | `/edge/v1/guest-access-plans` | site-owned |
| `/v1/voucher-batches...` (list/create/detail/codes/csv/revoke) | → /edge/v1 | `/edge/v1/voucher-batches...` | |
| `/v1/vouchers/{id}[,/revoke]` | → /edge/v1 | `/edge/v1/vouchers...` | |
| `/v1/sessions[/{id}, /disconnect]` | → /edge/v1 | `/edge/v1/sessions...` | disconnect goes straight to local scd — no NATS hop |
| `/v1/pms-providers...` (+test/cache/health) | → /edge/v1 | `/edge/v1/pms-providers...` | config + live probes are local |
| `/v1/walled-garden[/{id}, /effective]` | → /edge/v1 | `/edge/v1/walled-garden...` | |
| `/v1/notification-providers...` | → /edge/v1 | `/edge/v1/notification-providers...` | |
| `/v1/social-providers...` | → /edge/v1 | `/edge/v1/social-providers...` | |
| `/v1/stripe-accounts...` | → /edge/v1 | `/edge/v1/stripe-accounts...` | |
| `GET /v1/payments/` (admin history) | → /edge/v1 | `/edge/v1/payments` | |
| `POST /v1/checkout/create`, `GET /v1/checkout/{id}` (guest) | → edge | served on the appliance (portald→scd against the site DB) | guest checkout must work like every other guest flow: locally |
| `POST /v1/webhooks/stripe/{tenant_id}` | → edge | appliance-served webhook endpoint (see note) | Stripe must reach the site's public endpoint; until per-site exposure exists, a cloud relay forwards verified events — transitional |
| `GET|POST /oauth/stub/authorize-sso[/confirm]` | STAYS (dev-only stub IdP) | — | |

New cloud-only surfaces (no legacy ancestor): `/cloud/v1/fleet/*`,
`/cloud/v1/licenses/*`.

## 3. Deprecation mechanics

During the compatibility window the legacy guest-domain routes:

1. keep working against the central DB copies (frozen post-`sitemigrate` for
   migrated sites);
2. respond with `Deprecation: true` and
   `Link: <successor>; rel="successor-version"` headers;
3. are counted in metrics (`ctrlapi_http_*` by route) so removal is
   evidence-based — a route with zero traffic for the agreed window is safe to
   drop.

## 4. Removal plan (after pilot cutover)

Preconditions, then removal in one release:

- [ ] Pilot site fully cut over (scd/acctd on site DSN, edged serving Hotel
      Admin, phase suites green — [MIGRATION_RUNBOOK.md](MIGRATION_RUNBOOK.md)).
- [ ] Old `web-admin` replaced by `cloud-admin` + `hotel-admin`.
- [ ] E2E suites re-pointed: guest/hotel suites at `/edge/v1`, commercial
      suites at `/cloud/v1`.
- [ ] Legacy-route traffic at zero for the observation window.
- Then: delete the `/v1` guest-domain mounts from `router.go`, drop the frozen
  central guest-domain tables, and remove the legacy role values from the edge
  `operator_roles` check constraint
  ([ROLE_AND_SCOPE_MATRIX.md](ROLE_AND_SCOPE_MATRIX.md) §5).

The **appliance protocol** endpoints (`/v1/appliances/enroll`,
`/v1/appliance/hello`, `/v1/appliance/license`) are explicitly *not* part of
this removal — fielded appliances depend on those exact paths; any renaming
would require a coordinated appliance update cycle and is out of scope.

## 5. Impact on the E2E suites

The 25 phase suites assume the shared DB and `/v1` routes
([CURRENT_STATE_ASSESSMENT.md](CURRENT_STATE_ASSESSMENT.md) §9.4); the
compatibility adapters exist largely so they don't all go red at once.
Re-pointing plan:

| Suites | New target |
|---|---|
| phase 1, 2, 4.x, 6 (guest path) | unchanged — they exercise portald/scd, which move DSN, not routes |
| phase 3 (admin API), 5.6, 5.7 | split: commercial assertions → `/cloud/v1`, guest-domain CRUD → `/edge/v1` |
| phase 5.1–5.4 (enrollment/NATS/reload/heartbeat) | unchanged — appliance protocol stays |
| phase 8, 9, 10, 11 (providers) | provider CRUD → `/edge/v1` |
| phase 12 (payments) | checkout/webhook → edge-served endpoints |
| phase 13–15 (observability/TLS/alerting) | unchanged, plus new fleet-telemetry assertions |

New suites added by the refactor (isolation, offline, license state machine)
run natively against `/edge/v1` + `/cloud/v1` and have no legacy dependency.

## 6. Client migration notes

- `web-admin` callers: replace `/api/v1/...` with the split UIs; the tenant
  selector concept survives only in cloud-admin (Hotel Admin is single-site
  by construction and needs no `?tenant_id=`).
- Scripted API users (voucher exports, reports): move to
  `https://<appliance-mgmt-ip>/edge/v1/...` with a site-operator account —
  cloud credentials will stop reading guest data the day the adapters go.
- Error envelope, cursor pagination and `trace_id` semantics are identical in
  both new namespaces — only paths and auth domains change.
