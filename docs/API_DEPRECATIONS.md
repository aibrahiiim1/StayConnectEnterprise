# API Deprecations — Removed Routes and Their Replacements

> The pre-refactor ctrlapi exposed everything under one `/v1/*` namespace, and the
> first split kept deprecated compatibility adapters there for the pilot. **That
> compatibility window is closed.** The Central redesign (Product-Owner mission
> "Central Control Plane refactor & activation redesign", 2026-09-27) removed every
> legacy adapter together with the commercial, enrollment-token, SSO and telemetry
> surfaces. This file records what was removed and where each capability lives now.
> The current Central contract is [CENTRAL_CONTROL_PLANE.md §6](CENTRAL_CONTROL_PLANE.md#6-api-contract).

## 1. What ctrlapi serves now

Nothing else is mounted (`control-plane/internal/http/router.go`):

| Surface | Routes |
|---|---|
| Infra | `GET /healthz`, `GET /readyz`, `GET /metrics` (loopback only) |
| Operator session | `POST /v1/auth/login`, `POST /v1/auth/logout`, `GET /v1/auth/whoami`, `POST /v1/auth/reauth` |
| Operator console | `/cloud/v1/*` — overview, customers, sites, customer users, appliances (and their lifecycle actions), offline activation, licenses, security alerts, trust, audit, team, backup health |
| Appliance protocol | `POST /v1/appliances/register`; `GET /v1/appliance/hello`, `/license`, `/certificate`; `POST /v1/appliance/csr`, `/offline-reconcile`; mutual-TLS only: `GET /v1/appliance/assignment`, `/assignment-registry`, `POST /v1/appliance/assignment/ack` |

## 2. Removed from ctrlapi

| Removed | Now |
|---|---|
| Legacy client-domain `/v1/*` adapters (ticket templates, voucher batches, vouchers, sessions, PMS providers, walled garden, notification providers, social providers, Stripe accounts, payments) | On each appliance's Edge API `/edge/v1/*` against the site DB; Central holds no client domain (tables dropped, migration 0046) |
| `POST /v1/checkout/*`, `POST /v1/webhooks/stripe/{tenant_id}` (public checkout, Stripe webhook) | Removed from Central; client payment is an appliance concern |
| `GET/POST /v1/auth/sso/*`, `/oauth/stub/*`, `idp_providers` | Removed — Central sign-in is email and password only |
| `POST /v1/appliances/enroll`, `/cloud/v1/appliance-bootstrap-tokens` | Removed — the appliance registers itself (`POST /v1/appliances/register`, token-less); offline sites use offline activation |
| `/cloud/v1/tenants*` (incl. subscription, effective-limits, usage sub-routes), `/v1/tenants*` | `/cloud/v1/customers*` (`customer_id` is the old `tenant_id`) |
| `/cloud/v1/sites` (flat list/create) | `/cloud/v1/customers/{id}/sites`, `PATCH|DELETE /cloud/v1/sites/{id}`, `…/archive|restore` |
| `/cloud/v1/appliances` create (manual appliance creation), `…/effective-config` | Removed — appliances only register themselves; Central holds no appliance configuration |
| `/cloud/v1/commercial-plans`, `/v1/plans`, subscriptions, `tenant_limit_overrides` | Removed — the signed appliance licence is the only entitlement; the archived commercial history was deleted with schema `legacy_archive` (migration 0047) |
| `/cloud/v1/licenses` POST (site-scoped issue from a subscription) | `POST /cloud/v1/appliances/{id}/activate` and `POST /cloud/v1/appliances/{id}/license` |
| `/cloud/v1/operators*`, `/v1/operators*` | `/cloud/v1/team*` (Central operators) and `/cloud/v1/customers/{id}/users*` (customer users); site staff are `/edge/v1/operators` on the appliance |
| `/cloud/v1/fleet/*` (registry + telemetry) | `/cloud/v1/overview` and `/cloud/v1/appliances` (activation, connection and licence state derived by ctrlapi); telemetry is off (CLAUDE.md §0E, migration 0045) |
| Appliance deactivate / decommission / reconcile / claim endpoints | `POST /cloud/v1/appliances/{id}/retire` (two-phase or emergency), `…/move`, `…/replace`, `…/rebind-wan-mac`, `…/reissue-certificate`, `DELETE /cloud/v1/appliances/{id}` |
| A **cross-customer** `POST /cloud/v1/appliances/{id}/move` (licence revoked, appliance purged its local data in place) | Refused with `409 cross_customer_move`. Changing customer is Retire → factory-reset the appliance → it registers again → `…/activate` for the new customer. A same-customer move re-issues the licence with the same terms and answers `503 licensing_unavailable` when it cannot |
| Assignment-key state changes over HTTP | Host command `ctrlapi assignment-key verify-only|revoke --key-id <id> --reason <text> [--emergency]`; `GET /cloud/v1/trust` is read-only |
| `GET /v1/version`, `/cloud/v1/version` | `GET /readyz` reports the version |

The legacy `web-admin` console was removed; the Central console is `cloud-admin`,
and Admin Console is `hotel-admin` on each appliance.

## 3. Removed from the appliance (edged `/edge/v1`, scd socket)

| Removed | Now |
|---|---|
| `GET /edge/v1/license`, `POST /edge/v1/license/refresh` | `GET /edge/v1/central/status`, `POST /edge/v1/central/refresh` (Check now) |
| `/edge/v1/setup/*` (setup wizard, "Connect with token" enrollment) | Token-less registration by scd; `GET /edge/v1/central/offline-request`, `POST /edge/v1/central/offline-package` for offline activation |
| `/edge/v1/network/cloud*`, `/edge/v1/network/setup/*` | `GET /edge/v1/central/status` |
| scd socket `/v1/setup/*` (incl. `/v1/setup/enroll`) | scd socket `/v1/central/status`, `/v1/central/refresh`, `/v1/central/offline-request`, `/v1/central/offline-package`, `/v1/license/install` |
| `GET/PUT /edge/v1/cloud-sync-settings`, `GET/POST /edge/v1/cloud-sync-recovery` (delivered-record retention and recovery of the telemetry queue) and their role permissions | Removed with the telemetry subsystem (2026-09-27, appliance migration 0093). Nothing replaces them: there is no queue |
| `sync_outbox` figures in edged `GET /edge/v1/health`, and edged's `service_health` telemetry producer | Removed; local service health is still recorded in `appliance_service_health` and shown in Admin Console |
| scd socket `GET /v1/admin/outbox/stats` | Removed with the outbox |
| scd NATS subjects: RPC dispatcher and heartbeat, remote client-session revoke, remote PMS test / cache / health, tenant PMS config broadcast, nft set replication (`nft.<siteID>`), the signed command channel and the software-update agent; env `SCD_NATS_URL`, `SCD_NATS_MTLS_URL`, `SCD_COMMAND_PUB`, `SCD_UPDATE_PUB` | Removed — the appliance has no message-bus client (CLAUDE.md §0E). Session revoke, PMS operations and configuration are local, in Admin Console |
| `POST /edge/v1/network/adopt` and netd's `POST /v1/adopt` | `POST /edge/v1/network/apply` then `POST /edge/v1/network/revisions/{id}/confirm`. Adopt recorded the editable `guest_networks` rows as the ACTIVE revision without applying or validating them and without checking that the live Linux network matched — and the active revision is what netd's boot reconcile asserts as the truth, so an unapplied draft could become the appliance's definition of reality at the next reboot. It existed for two reasons that are both finished: importing a legacy running system that already matched, and seeding a first rollback target (rollback handles having none). Nothing ever called it: no screen, script, runbook or test, and zero uses in the only appliance's whole history. It could not be made honest either — proving that the live network matches an intent means asserting it, which is an apply. A client network configuration now becomes active by being applied, health-checked and confirmed, and by no other route |

`POST /edge/v1/license` (licence file upload) stays. While the appliance is *Removed from OneGate Central* it
answers `409 removed_from_central`, as do `POST /edge/v1/central/offline-package` and the scd install routes.

## 4. Old console addresses

Both consoles answer old page addresses with a permanent redirect (308):

- **Central** (`cloud-admin/next.config.mjs`): `/dashboard` → `/overview`; `/tenants`, `/sites` →
  `/customers`; `/onboarding` → `/appliances?activation=waiting`; `/operators` → `/system/team`;
  `/security` → `/system/security-alerts`; `/certificates`, `/assignment-keys` → `/system/trust`;
  `/backup-health` → `/system/backup-health`; `/audit` → `/system/audit`; `/commercial`, `/subscription` →
  `/licenses`.
- **Admin Console** (`hotel-admin/next.config.mjs`): `/license`, `/network/cloud`, `/setup/enrollment` →
  `/appliance`.
