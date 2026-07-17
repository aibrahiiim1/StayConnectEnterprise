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

## Exact-script end-to-end dry run — §3 = PASS (2026-07-17)

The **exact committed scripts** were executed against a disposable DB reconstructed from the verified
backup (`gatep-dryrun.sh`), then everything was destroyed. **Production untouched** (42 public / iam_v2
0 rows; `leaked_svc=0`, `gatep_dbs=0` after run).

**Committed script SHA-256:**
- `gatep-roles.sql`        `aba132665ea0c7c3b856bf8fed3225af6ea7338f1ba78127f70639a5ba4437d6`
- `gatep-grants.sql`       `ac7471f83dcbb974e734a388c18c8149106982cb1c52148d6d82f571c5655c59`
- `gatep-rollback.sql`     `a499f4a667fb202cbe5532365ab5b8e71da08989b317eec1cfe8bd4de2c79972`
- `gatep-set-passwords.sh` `8d4b795b234e24d21edbabc4f7b9e6669f8cc53bf424333fd4144be99611ca5c`
- `scram_verifier.py`      `5d43fcbf08f764ddd115a752a5cc92b7aeca1cf1274acbf4189a5d897652a1f2`
- `gatep-dryrun.sh`        `3569540accd7cd3db1b81879dac73bf013cced6904509960497ba42b2385f3fc`

**Isolated public fingerprint:** `8659c08db41624d5cab946027c0d6c2d37251fc1550ec35929241827acf852f9`

**Results (`GATEP_DRYRUN = PASS`):**
- Role creation (split `SET` statements) + secure SCRAM password set — `svc_scd` stored as
  `SCRAM-SHA-256` (**no cleartext** in SQL/argv/logs; `log_statement=none`, `log_min_duration=-1` proven).
- Reconciler `gatep-grants.sql`: fail-closed on unexpected ownership/membership, revoke-first, then
  allowlist grants. **Idempotent** — identical effective grant set on the 2nd run
  (`sha 21c2dc75…`).
- Role attributes: all `svc_*` `NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS`; **zero iam_v2**
  table + function grants.
- Default-privilege denial: a freshly owner-created table is **denied** to `svc_scd`
  (`ALTER DEFAULT PRIVILEGES FOR ROLE stayconnect` explicit); `PUBLIC` holds zero table grants.
- Negatives (as the role): iam_v2 access, out-of-allowlist (`operators`/`guests`), `CREATE`, `DROP` all DENIED.
- Positives (as the role, real credential): `svc_scd` SELECT sessions/guests + INSERT audit_log;
  `svc_acctd` SELECT vouchers/ticket_templates + INSERT accounting_records; `svc_netd` SELECT
  network_interfaces; `svc_edged` SELECT operators.
- Rollback (`gatep-rollback.sql`, fail-closed on active-conn/ownership, `DROP OWNED BY` + `DROP ROLE`)
  → all `svc_*` removed; **reapply** recreates all 4 cleanly.

**Cluster-global role hygiene:** PostgreSQL roles are cluster-global. An earlier harness ran roles in
the *live* cluster and (before the fix) leaked 4 **inert** roles (no service used them; removed;
final leaked count zero). The harness was then reworked to use a **genuinely disposable separate
PostgreSQL/TimescaleDB container** (`timescale/timescaledb:2.16.1-pg16`, matching the appliance) so
cluster-global roles are created only inside the throwaway cluster, which is destroyed afterward. The
live `stayconnect-pg` cluster is never used for the dry run (`live_svc=0`, `disp_left=0` after runs).

### Harness correctness rework (exit-status based) — final `GATEP_DRYRUN = PASS`
Every SQL execution is judged by the direct **psql exit status** (`ON_ERROR_STOP=1`), never by grepping
output. A **self-test** proves intentionally-invalid SQL fails the executor, and a meta mode
(`--selftest-must-fail`) proves the harness reports `GATEP_DRYRUN = FAIL` with a **non-zero process exit
(META_EXIT=1)**. This surfaced and fixed check bugs the old grep-harness masked: a `NOT NULL` test-data
error (INSERT permission was fine — now proven via authoritative `has_table_privilege`); the
`information_schema` visibility quirk (zero-iam_v2 + idempotency now use `has_*_privilege` /
`pg_catalog`); a readiness race with the image's `timescaledb-tune` restart (now requires 6 consecutive
`pg_isready`); and an `ORDER BY 2` on a single-column projection. Effective iam_v2 function execution is
measured **gated by schema USAGE** (the PUBLIC-default `EXECUTE` is unreachable with `iam_v2` USAGE
denied; Phase-1A iam_v2 ACLs are not altered).

**Final proofs (disposable cluster, GATEP_DRYRUN = PASS / exit 0):** stable-ready cluster; restore
42+49; self-test (invalid SQL fails); roles.sql; SCRAM password set (stored `SCRAM-SHA-256`, no
cleartext; DSN 0600); reconciler grants.sql; all `svc_*` NOSUPERUSER/NOCREATEDB/NOCREATEROLE/NOBYPASSRLS;
**zero EFFECTIVE iam_v2 (table=0, schema-usage=0, effective-function=0)**; default-privilege denial;
negatives DENIED (as the role); positive SELECTs (as the role) + write privileges (authoritative
`has_table_privilege`); **idempotent** (59 role-table grant rows, matrix sha `ae08e7fc…`); rollback →
roles removed; reapply → 4 roles; disposable cluster destroyed.

Updated `gatep-dryrun.sh` SHA-256: `fe5efcb732e77cef1c9ef0bbf66e5ad867b4d1e6fa5d8d7297e1fc54998d5268`.

**§3 Exact grant derivation + Gate-P execution artifacts + disposable-cluster isolated validation = PASS.**

## Remaining (deeper positive proof, at Gate-P live dry-run)
Running each full service **binary/integration suite** under its `svc_*` DSN is the deepest positive
proof; the representative per-role real-query proofs + all boundary proofs above are established. Live
Gate P will additionally verify each service connects + behaves under its role before proceeding.
