# Phase 1B — Gate P least-privilege grant derivation (evidence)

**Status: IN PROGRESS (Phase 1B, dark).** Evidence-based derivation of the exact per-service
`public`-schema privileges for the Gate-P `svc_*` roles, from the **real Go source** and validated
on an **isolated reconstruction** of the production schema. Authoritative target grant matrix:
[`../architecture/Phase1B-Privilege-Matrix.md`](../architecture/Phase1B-Privilege-Matrix.md).
Binding: production runtime roles receive `public` privileges only — **zero `iam_v2`** (schema,
table, sequence, function) — and no unnecessary `DELETE`. `PHASE_1B_PRODUCTION_IAM_V2_RUNTIME: NONE`.

## Method
1. **Import graph:** for each DB-connecting service (`scd`, `edged`, `acctd`, `netd`) the set of
   imported `internal/*` packages was extracted from `data-plane/cmd/<svc>/*.go`.
2. **Table usage:** the service's `cmd` files plus every imported package directory were scanned for
   references to each of the 42 production `public` base tables (SQL `FROM/INTO/UPDATE/JOIN/…`).
3. **Isolated validation target:** `stayconnect_site_b` is **stale (28 tables vs production 42)** and is
   **NOT** used as the validation target. Instead an **isolated disposable DB is reconstructed from the
   verified Gate-P backup** (`/var/backups/stayconnect/phase1b-gatep-20260717-151210/stayconnect_site.dump`,
   already proven to restore to 42 public + 49 iam_v2 tables). Roles + grants are applied there; positive
   service queries and negative (outside-allowlist) queries are run; the matrix is corrected from results.
4. `portald` and `hotel-admin` have **no DB URL** (confirmed in `/etc/stayconnect/*.env`) — **no DB role**.

## Code-derived table usage (real evidence, 2026-07-17)

### `svc_scd` (site-DB session/auth/credential/appliance)
`appliances`, `audit_log`, `auth_otps`, `edge_executed_commands`, `edge_installed_updates`,
`edge_offline_packages`, `guest_accounts`, `guest_networks`, `guests`, `notification_providers`,
`pms_attempts`, `pms_providers`, `sessions`, `sites`, `social_oauth_providers`, `social_oauth_states`,
`sync_checkpoints`, `sync_outbox`, `tenant_effective_limits`, `tenants`, `ticket_templates`, `vouchers`,
`walled_garden_rules` (+ new `auth_throttle_buckets`, §4b).

> **Reconciliation vs matrix §1.1:** matrix must ADD `appliances`, `sites`, `edge_executed_commands`,
> `edge_installed_updates`, `edge_offline_packages` (appliance-auth / command-channel / updates paths
> that scd owns via `applianceauth`/`commands`/`updates`/`identity` imports). To confirm read-vs-write
> split per table during isolated validation.

### `svc_edged` (admin API / Hotel-Admin backend)
`appliance_boot_convergence`, `appliance_recovery_events`, `appliance_service_health`, `audit_log`,
`backup_records`, `dhcp_pools`, `dhcp_reservations`, `guest_accounts`, `guest_networks`,
`network_apply_events`, `network_config_revisions`, `network_health_checks`, `network_interfaces`,
`notification_providers`, `operator_roles`, `operators`, `payments`, `pms_providers`, `sessions`,
`social_oauth_providers`, `stripe_accounts`, `sync_checkpoints`, `sync_outbox`,
`tenant_effective_limits`, `tenants`, `ticket_templates`, `voucher_batches`, `vouchers`,
`walled_garden_rules`.

### `svc_acctd` (accounting)
`accounting_records`, `sessions` (+ candidate `ticket_templates`, `vouchers` — flagged for isolated
confirmation; likely read-only via shared `shape`/`identity` helpers, to be pinned by negative test).

### `svc_netd` (networking only — no credentials)
`dhcp_pools`, `dhcp_reservations`, `guest_networks`, `network_apply_events`, `network_config_revisions`,
`network_health_checks`, `network_interfaces`, `system_network_audit`.

## Migration / executor roles
- `iam_v2_owner` — NOLOGIN, owns `iam_v2` (exists in production).
- `iam_v2_migrator` — NOLOGIN, member of owner (SET ROLE), migrations only.
- `site_migrator` — NOLOGIN, owns `public` migrations.
- `migrate_exec` — LOGIN, drives migrations via SET ROLE; enabled only in the migration window, audited,
  rotated + disabled after. Holds **no** runtime service privileges.
- `stayconnect` superuser DSN — **break-glass Gate-P rollback only** (time-bounded, audited, removed
  after Gate-P acceptance).

## Per-role attributes (all runtime `svc_*`)
`LOGIN`, `NOSUPERUSER`, `NOCREATEDB`, `NOCREATEROLE`, `NOBYPASSRLS`, no object ownership,
`CONNECTION LIMIT`, `statement_timeout`/`lock_timeout`/`idle_in_transaction_session_timeout` via
`ALTER ROLE … SET`, `ALTER DEFAULT PRIVILEGES` denying future owner objects, and **zero `iam_v2`
privileges**. No `GRANT … ON ALL TABLES` shortcuts — every grant is table-scoped.

## Remaining (this workstream)
1. Build table-scoped `GRANT` scripts (`SELECT`/`INSERT`/`UPDATE`, `DELETE` only where code needs it,
   sequence `USAGE` only for tables written).
2. Reconstruct the isolated DB from the verified backup; create the roles; apply grants.
3. Run positive real-query samples per service + negative outside-allowlist tests; prove `rolsuper=false`
   and zero `iam_v2` access.
4. Correct `Phase1B-Privilege-Matrix.md` from the isolated-validation results (add the scd appliance/
   command/updates tables; confirm/trim acctd candidates).
