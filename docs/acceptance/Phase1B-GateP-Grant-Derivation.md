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

## Write-verb evidence (real SQL, per service)
Extracted from each service's reachable code (`cmd` + imported packages). `SELECT` is granted on every
referenced table; write verbs below are the evidenced `INSERT/UPDATE/DELETE`. Upserts
(`INSERT … ON CONFLICT DO UPDATE`, e.g. scd `guests` by `(tenant,mac)`) require `INSERT+UPDATE` even
though text-search shows only `INSERT` — pinned by the isolated positive test, not by grep alone.
- **acctd** resolved: `vouchers` + `ticket_templates` are **genuinely required** (read-only `LEFT JOIN`
  in the quota query `acctd/main.go:303-304`) → `SELECT` only. Kept.
- **scd** edge tables resolved (evidenced verbs): `appliances` INSERT, `sites` INSERT+DELETE,
  `edge_executed_commands` INSERT, `edge_installed_updates` SELECT+INSERT, `edge_offline_packages`
  INSERT+UPDATE → matrix updated to add these five with these verbs.

## Isolated validation RESULT (2026-07-17) — PASS
Reconstructed a disposable DB from the verified backup (`42` public + `49` iam_v2), created the four
`svc_*` roles, applied `deploy/gatep/gatep-grants.sql`, then dropped it. **Production untouched.**
- Role attributes: `svc_scd/edged/acctd/netd` all `NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS`. ✓
- **Zero iam_v2 privileges** for every runtime role (`svc_all_iam_v2_grants = 0`). ✓
- Negative (all DENIED): `svc_scd→iam_v2.guest_principals`; `svc_scd→public.operators`;
  `svc_netd→public.guests`; `svc_acctd→public.guest_accounts`; `svc_scd CREATE TABLE`; `svc_scd DROP TABLE`. ✓
- Positive (OK): `svc_scd SELECT sessions`; `svc_acctd SELECT vouchers/ticket_templates`;
  `svc_netd SELECT network_interfaces`; `svc_scd INSERT audit_log` privilege confirmed (failed only on a
  test column typo — a post-permission column-resolution error). ✓

## Artifacts
- `deploy/gatep/gatep-roles.sql.tmpl` — role creation + attributes (passwords injected at runtime; no secret committed).
- `deploy/gatep/gatep-grants.sql` — exact table-scoped grants + per-table sequence USAGE (no ALL-TABLES/ALL-SEQUENCES).
- `deploy/gatep/gatep-rollback.sql` — REVOKE + DROP ROLE (roles own nothing; DSN revert precedes it).

## Remaining (full positive proof)
Full per-query positive proof by running each service's integration suite against the isolated DB under
its `svc_*` role is the deeper validation to complete during Gate P dry-run; the boundary proofs
(zero iam_v2, no-superuser, no-outside-allowlist, no DDL) are already established above.
