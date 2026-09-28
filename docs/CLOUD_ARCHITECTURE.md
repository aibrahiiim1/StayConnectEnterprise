# Cloud Architecture

> The vendor half of the edge-first split. Central manages customers, sites,
> appliance activation, licences and fleet status. It never serves a client,
> never stores client PII and never controls an appliance remotely (CLAUDE.md
> §0E: licensing only). The console structure, appliance states, lifecycle, API
> contract and roles are specified in
> [CENTRAL_CONTROL_PLANE.md](CENTRAL_CONTROL_PLANE.md), which wins over this file
> wherever they differ. Edge counterpart: [EDGE_ARCHITECTURE.md](EDGE_ARCHITECTURE.md).

## 1. Services

| Service | Binary / stack | Role |
|---|---|---|
| `ctrlapi` | Go (`control-plane/`), `:8080` behind Caddy; appliance mutual-TLS listener `CTRLAPI_MTLS_ADDR` (default `:9443`) | Operator API `/cloud/v1/*`, authentication `/v1/auth/*`, appliance protocol `/v1/appliances/register` + `/v1/appliance*`. Nothing else is mounted. |
| `cloud-admin` | Next.js standalone UI, `:3000` behind Caddy | The Central console: Overview · Customers · Appliances · Licenses · System |
| Caddy | `deploy/caddy/Caddyfile.central` | TLS; `/v1/* /cloud/* /healthz /readyz` → ctrlapi, everything else → cloud-admin |
| Postgres + TimescaleDB | `stayconnect` DB | Central-owned tables only (see §2) |
| Redis | operator session store | Opaque `sc_session` cookies |
| Observability | Prometheus / Grafana / Alertmanager | `ctrlapi_*` metrics (`/metrics`, loopback only) |
| Vendor signing key | `CTRLAPI_VENDOR_KEY` (64-byte Ed25519 private key file, 0600) | Signs licence documents. Exists **only** on Central; appliances hold public keys only. When absent, licence issuing and fetching are disabled. |
| Assignment signing key | `CTRLAPI_ASSIGN_KEY` (dedicated Ed25519 key; refused if equal to the vendor key) | Signs the assignment binding an appliance to its customer and site. When absent, activation, moves and retirement refuse. |
| Registry root key | `CTRLAPI_REGISTRY_ROOT_KEY` | Signs the versioned trust registry of assignment keys the appliance verifies. |
| Appliance CA | offline root → online intermediate (`CTRLAPI_INTERMEDIATE_CA_KEY`) | Issues appliance client certificates; runtime uses only the intermediate. |

There is **no message bus**: ctrlapi opens no NATS connection and consumes no
telemetry, and the appliance has no client for one. Everything an appliance
needs from Central is HTTPS. The host is installed and upgraded by
`deploy/scripts/central-install.sh` (backing services in
`deploy/compose/central-infra.yml`, units `stayconnect-ctrlapi`,
`stayconnect-cloud-admin`, `stayconnect-central-backup.timer`); the runbook is
[DEPLOYMENT_CLOUD.md](DEPLOYMENT_CLOUD.md).

Host commands (`ctrlapi <cmd>`): `serve` (default), `seed-admin`,
`gen-vendor-key`, `gen-assignment-key`, `gen-registry-key`, and
`assignment-key verify-only|revoke --key-id <id> --reason <text> [--emergency]`
(the console's **System → Trust & keys** is read-only).

## 2. Central database ownership

The Central DB holds exactly the licensing, activation and fleet-status domain:

| Table(s) | Contents |
|---|---|
| `tenants` | customers (API name `customer_id`) |
| `sites` | properties per customer |
| `appliances` | inventory: serial, hardware fingerprint, identity public key, WAN/LAN MAC, `lifecycle_state` (`pending_approval`, `assigned`, `revoked`, `decommissioned` — migration 0046), `registered_at` (renamed from `enrolled_at` by 0047), `last_seen_at`, `activated_at`, replacement fields |
| `retired_appliance_identities` | identity keys of retired appliances (migration 0047); registration and offline import refuse them (`identity_retired`), so a retired box must be factory-reset before it can be activated again |
| `appliance_assignments`, `appliance_signed_assignments`, `appliance_assignment_history`, `appliance_assignment_fetch_log` | the signed customer/site binding and its history (fetch log pruned after 30 days) |
| `assignment_signing_keys`, `assignment_registry` | assignment keys (active / verify_only / revoked) and the signed registry |
| `appliance_certificates`, `appliance_certificate_requests`, `appliance_certificate_events`, `appliance_certificate_revocations`, `appliance_ca_versions` | appliance PKI |
| `appliance_terminal_delivery` | two-phase retirement state |
| `appliance_lifecycle_events`, `appliance_security_alerts` | lifecycle history; clone / hardware-reuse / WAN-MAC / unconfirmed-retirement alerts |
| `offline_activation_requests`, `offline_activation_packages` | offline activation |
| `licenses` | signed licence envelopes + queryable projection; **one current (`active`/`suspended`) licence per appliance**, enforced by a partial unique index |
| `operators`, `operator_roles` | Central sign-ins (platform Team and customer users) |
| `audit_log` (hypertable) | every Central write |


Client-domain data (guests, sessions, vouchers, PMS, payments, OTP, portal, …)
is **edge-owned** and does not exist on Central: the empty legacy tables were
dropped by migration 0046, and the fleet telemetry tables by migration 0045.
Migration 0047 dropped the `legacy_archive` schema with everything in it (the
retired commercial history 0046 had moved there and an older client-history
archive) and the columns nothing read any more (operator SSO, tenant sign-in
methods and metadata, the licence table's plan/features/limits copies,
site-scoped operator roles). Full matrix: [DATA_OWNERSHIP.md](DATA_OWNERSHIP.md).

## 3. API surface

The complete contract — every `/cloud/v1/*` route, its body, response and
step-up marking — is [CENTRAL_CONTROL_PLANE.md §6](CENTRAL_CONTROL_PLANE.md#6-api-contract).
In short:

- `/v1/auth/login|logout|whoami|reauth` — operator session (`sc_session`).
- `/cloud/v1/overview`, `/customers`, `/sites`, `/appliances` (activate, move,
  retire, replace, rebind-wan-mac, reissue-certificate, delete, license,
  offline-license, offline-activation-package), `/offline-activation/requests`,
  `/licenses` (suspend, resume, revoke), `/security-alerts`, `/trust`,
  `/audit`, `/team`, `/customers/{id}/users`, `/backup-health`.
- Appliance-facing (Ed25519 appliance JWT with replay protection, and mutual
  TLS once a certificate is issued): `POST /v1/appliances/register`
  (token-less, self-signed), `GET /v1/appliance/hello|license|certificate`,
  `POST /v1/appliance/csr|offline-reconcile`,
  `GET /v1/appliance/assignment|assignment-registry`,
  `POST /v1/appliance/assignment/ack`.

Removed endpoints are recorded in [API_DEPRECATIONS.md](API_DEPRECATIONS.md).

## 4. Fleet status without telemetry

Central derives every appliance's **activation**, **connection** and
**licence** state on each read (`control-plane/internal/api/state.go`,
`fleet.go`); none of it is stored as a lifecycle value. Connection comes from
`last_seen_at`, which every authenticated appliance call updates. Appliances
send no usage, health or heartbeat telemetry (CLAUDE.md §0E).

A background loop (every minute) flags retirements not acknowledged within
10 minutes (`terminal_delivery_failed` + a security alert; credentials are
**not** revoked), raises an alert when a replacement window (72 hours) elapses,
and hourly prunes assignment-fetch log rows older than 30 days.

Retirement — by **Retire** or by completing a hardware **replacement** — is
always the acknowledged two-phase terminal delivery
(`internal/api/terminal_delivery.go`): the licence is revoked, a signed terminal
assignment is issued, and the appliance's credentials are revoked only after its
signed acknowledgement or on an emergency retire. A **move** stays within the
customer (`409 cross_customer_move` otherwise) and re-issues the licence for the
new site with the same terms in the same transaction, failing closed
(`503 licensing_unavailable`) when that is not possible.

## 5. Licensing issuance flow (`internal/licensing`)

```
platform admin ──POST /cloud/v1/appliances/{id}/activate──▶ ctrlapi   (or …/{id}/license to renew/change)
   │ 1. step-up (recent password) + platform role
   │ 2. activate: appliance must be waiting; customer/site created inline if asked;
   │    signed assignment written in the same transaction
   │ 3. read the appliance's registered binding: serial, hardware fingerprint,
   │    identity key fingerprint, WAN MAC
   │ 4. build Document{schema_version 3, appliance binding, max_concurrent_online_guests,
   │      valid_from/valid_until, grace_period_days, license_version = previous + 1,
   │      supersedes_license_id, all features, commercial_plan_code "direct"}
   │ 5. Signer.Sign → Envelope{payload_b64, signature, key_id}
   │ 6. tx: supersede the appliance's current licence; insert the new row
   ▼
audit_log: license.issued          appliance pulls it via GET /v1/appliance/license
```

Revocation: `POST /cloud/v1/licenses/{id}/revoke` sets `status='revoked',
revoked_at=now()`; the appliance's next licence fetch returns its revoked
licence IDs alongside the current envelope, and the edge persists them in its
local revocation store. Suspend/resume re-issue with `status` suspended/active.
None of these touches the appliance's lifecycle, so a suspended or revoked
appliance keeps fetching. Details and the edge-side state machine:
[LICENSING_AND_ENTITLEMENTS.md](LICENSING_AND_ENTITLEMENTS.md).

## 6. What the cloud must never do

- Serve a captive portal or authorize a client session.
- Store or receive client PII.
- Open a connection *to* an appliance — all links are appliance-initiated
  HTTPS (registration, assignment, certificate, licence, hello).
- Be a runtime dependency of the client path: an appliance with a valid signed
  license operates fully with the cloud unreachable ([OFFLINE_OPERATION.md](OFFLINE_OPERATION.md)).
