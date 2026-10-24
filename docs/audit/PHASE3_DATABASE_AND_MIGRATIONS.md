# Backend audit — Phase 3: database and migrations

| | |
|---|---|
| Audited commit | `9ffa981b` (master after PR #229; unchanged when this phase started) |
| Scope | Central migrations (`control-plane/migrations`, 56 pairs) and their runner `deploy/scripts/central-migrate.sh`; appliance migrations (`data-plane/migrations`, 150 pairs, the production baseline and `iam_base`) and their runner `scripts/edge-migrate.sh`; the schema build scripts (`generate-production-baseline.sh`, `clean-install-reconstruction.sh`, `lib-fullschema-testdb.sh`); `data-plane/internal/deployment` and `buildprofile`. |
| Code changed by the audit | none |
| Status | findings recorded; no fix applied |

## How to use this file

1. **Format.** It is the same as Phases 1 and 2. Each finding has a problem, its impact, the recommended fix and the test that proves it. Fixes are grouped into batches at the end.
2. **Almost everything here is CONTROLLED.** A schema change or a migration-runner change is "database schema or migration" under CLAUDE.md §0. The runners also decide how PRE-LIVE and every future appliance is upgraded.
3. **Never edit an applied migration.** Both runners treat a changed applied file as tampering. Central refuses it, and the appliance relies on `--expect-sha256`. Every schema fix below is a **new** migration.
4. **Status of a finding:**
   - **Confirmed**: traced in the code, or reproduced on the disposable database.
   - **Possible**: the code suggests the problem, but it needs a test or deployment facts to prove.
5. **How it was checked.**
   - The auditor read both runners and the build scripts in full.
   - Both schemas were built in throwaway TimescaleDB 2.16.1 / PostgreSQL 16 containers, the same image Central and the appliance use, and inspected through the catalog.
   - Two independent reviewers each read a share: Central's migrations and the runners, and appliance migrations 0120–0150. The auditor re-checked their claims on the live disposable database.
   - Appliance migrations 0001–0119 were covered by the catalog checks and the drift check, not read line by line.

## Summary

| Severity | Count |
|---|---|
| Critical | 0 |
| High | 0 |
| Medium | 6 |
| Low | 12 |
| Clean code | 4 |

### What was run

| Check | Result |
|---|---|
| Central: apply 0001–0056 with `central-migrate.sh up`, then `verify` | pass: 56 applied, 0 pending, 0 modified |
| Central: every `.down.sql` in reverse, then `up` again | pass: down to an empty schema; the re-applied schema is identical to the first (`pg_dump -s` diff empty) |
| Central: `go test ./...` with `CTRLAPI_TEST_DB_URL` and `CTRLAPI_TEST_REDIS_URL` | pass. The 33 database tests skipped in Phase 0 now run. Only `TestPebbleEndToEnd` skips (needs an ACME test server). |
| Appliance: factory-clean baseline build (`lib-fullschema-testdb.sh`, same order as provisioning) | pass |
| Appliance: full upgrade path 0001→0150 (`clean-install-reconstruction.sh`) | pass |
| Appliance: **drift**, baseline path against upgrade path (`pg_dump -s` of `public` and `iam_v2`) | **no drift**. The 40 diff lines are only redundant brackets inside equivalent CHECK expressions. |
| Appliance: 0150→0120 down, then 0120→0150 up | pass; schema identical except grant order and brackets |
| Appliance: `otp`, `throttle`, `keybootstrap`, `iamv2` database tests | `throttle` and `iamv2` pass. **7 `otp`/`keybootstrap` tests fail** on stale fixtures (M5). |
| Catalog checks | below, and in the findings |
| `staticcheck`, `gosec`, `go vet` on `deployment`, `buildprofile` | clean; `deployment` unit tests pass |
| `govulncheck` | only the Phase 0 P0-2 advisories, reached through `deployment`'s imports |

### What holds up well (keep it this way when fixing)

- **The two install paths agree.** A new appliance (baseline) and an upgraded one (0001→0150) have the same schema object for object, including grants and revokes. The baseline generator's handling of extensions, roles, hypertables and privileges works.
- **Down migrations work.** All of Central's, and the recent 31 on the appliance, undo their up exactly.
- **Appliance definer functions:**
  - all 273 `SECURITY DEFINER` functions in `public`/`iam_v2` pin `search_path`;
  - none is executable by PUBLIC.
- **Appliance roles:**
  - no service role is a superuser or has `BYPASSRLS`;
  - only the three IAM owner roles can create objects in `iam_v2`, and nobody else can in `public`.
- **Appliance history tables:**
  - no service role can update or delete `audit_log`;
  - stay events, posting attempts and entitlements are guarded by triggers, plus column grants (runtime roles may update only `entitlements.status`).
- **`edge-migrate.sh` is careful:**
  - positive target identity;
  - canonical-directory and symlink checks;
  - a ledger-wide advisory lock;
  - the file checksum re-verified just before execution;
  - downs only from the head of the ledger, with their own acknowledgement;
  - a mandatory pre-change backup on a live site.
- **Central's 0046 redesign drops data only behind a row-count guard**, and archives commercial history instead of dropping it.

## Medium

### M1 — Central's application connects to its database as the superuser that owns every table

**Status:** Confirmed · CONTROLLED (database schema, deployment)

**Where:**
- `deploy/env/ctrlapi.env.example:24` (`CTRLAPI_DB_URL=postgres://stayconnect:...`) and `deploy/compose/central-infra.yml:44` (`POSTGRES_USER: stayconnect`, which the image creates as superuser).
- On the migrated database, `stayconnect` is the only role, `rolsuper = t`, and it owns `audit_log`, `licenses`, `portal_tls_events` and every other table.

**Problem:**
- ctrlapi runs every query as a database superuser.
- The append-only rule on `portal_tls_events` (`0054:76-84`) is lifted by `SET central.retention = 'on'`, which any session can set.
- `audit_log` has no append-only guard at all, although `0037:87` calls it "the immutable audit_log".

**Impact:**
1. Any SQL injection or code-execution bug in ctrlapi becomes full control of the database: read every licence and signing record, rewrite or delete the audit trail, alter triggers.
2. It also gives operating-system command execution inside the database container (`COPY ... TO PROGRAM` is open to superusers).
3. The audit trail cannot be trusted after an incident, because the application could have rewritten it.
4. Phase 1 found no SQL injection in ctrlapi. This is defence in depth for the day one appears.

**Fix:**
1. Add a Central migration that creates two roles:
   - `ctrlapi_owner` (NOLOGIN) owns the schema;
   - `ctrlapi_app` (LOGIN, NOSUPERUSER) gets only the DML it needs.
2. Revoke `UPDATE`, `DELETE` and `TRUNCATE` on `audit_log` and `portal_tls_events` from `ctrlapi_app`.
3. Add an append-only trigger to `audit_log`.
4. Move the retention and forget paths (`central_retention_apply`, `central_forget_actor`) into `SECURITY DEFINER` functions owned by `ctrlapi_owner`, with `SET search_path = public, pg_temp`, and grant `ctrlapi_app` EXECUTE on them only. These functions set `central.retention` themselves; the trigger checks `current_user` = owner as well as the setting.
5. `central-install.sh` creates the login password for `ctrlapi_app` and writes it to `CTRLAPI_DB_URL`. Migrations keep running as the superuser through `central-migrate.sh`.

**Test:**
- As `ctrlapi_app`: `UPDATE audit_log`, `DELETE FROM portal_tls_events` (even after `SET central.retention='on'`), `CREATE TABLE` and `COPY ... TO PROGRAM` all fail.
- The full Central suite passes with `CTRLAPI_TEST_DB_URL` pointing at `ctrlapi_app`.
- PRE-LIVE smoke check after the Central upgrade (sign-in, licence fetch, assignment fetch).

### M2 — On a baseline-built appliance, the runner will re-run a migration the baseline already contains

**Status:** Confirmed (reproduced) · CONTROLLED (migration)

**Where:**
- `scripts/edge-migrate.sh:286-347` (`verify_no_gap_below`) and `:589` (the `need` test, which looks only at the ledger).
- `scripts/generate-production-baseline.sh` writes no ledger rows.
- The baseline's last covered version is a constant in two places: `scripts/lib-fullschema-testdb.sh:24` and `scripts/sitedb-dev.sh:20` (both `0149`).

**Problem:**
- A factory-clean appliance has ledger rows only for migrations applied after the baseline (today, 0150).
- The gap check counts only versions at or above the lowest ledger row, and the apply decision asks only "is this version in the ledger".
- So a version the baseline already applied looks new.

**Reproduced:** on a baseline-built copy, `edge-migrate.sh --only 0140_... --expect-sha256 <its sha>` passed every check. It printed `no gap below 0140 ... (0 checked)` and executed 0140. Only 0140's own `CREATE FUNCTION` (not `OR REPLACE`) stopped it.

**Impact:**
1. A migration made only of `CREATE OR REPLACE` statements would run to completion and **silently replace newer function bodies with old ones**.
   - 0091 is one: it re-creates `trg_catalogue_revision_immutable`.
   - Re-running it on an appliance would restore whichever trigger logic 0091 had, over every later version, and record the version as applied.
2. The trigger is easy to pull: an operator re-running a documented `--only` command, or a deploy loop over a list that includes old versions.
3. Every new appliance is built from the baseline, so every one is exposed.

**Fix:**
1. **Record what the baseline covers.** `generate-production-baseline.sh` appends `INSERT INTO public.schema_migrations(version) VALUES (...)` for every version up to the baseline's last migration, in the baseline file itself. The script's own comment at `edge-migrate.sh:314-317` already recommends this.
2. Derive the baseline top from the baseline header, in one place, instead of the two `0149` constants.
3. In `edge-migrate.sh`, refuse to apply any version lower than the highest numbered version in the ledger unless `--allow-out-of-order` is given. The gap check's "empty ledger" branch is then no longer needed.
4. Existing baseline-built appliances (PRE-LIVE, if it was built that way): a one-time, audited `adopt` step records 0001–0149. This is the appliance equivalent of `central-migrate.sh adopt`.

**Test:**
- `edge-migrate-selftest.sh` cases:
  - on a baseline build, `--only <any covered version>` is refused;
  - a fresh build's ledger lists every covered version;
  - the next new migration still applies.
- `factory-clean-baseline-verify.sh` checks that the ledger matches the header.

### M3 — The ledger row is not written in the same transaction as the migration, in either runner

**Status:** Confirmed (code; the crash window was not reproduced) · CONTROLLED (migration)

**Where:**
- **Central:**
  - `deploy/scripts/central-migrate.sh:163-182`: the migration runs in one psql call (`:174` or `:176`) and the ledger `INSERT` in a second (`:178`).
  - There is no advisory lock anywhere in the script.
- **Appliance:**
  - `scripts/edge-migrate.sh:612-613`: the ledger `INSERT` is appended after the file, and the file carries its own `BEGIN; ... COMMIT;`, so the insert commits separately.
  - 9 appliance migrations have no `BEGIN` at all and are not wrapped by the runner, so each statement commits on its own: 0003, 0004, 0006, 0007, 0008, 0047, 0048, 0049, 0091.

**Problem:**
- A crash, `kill`, SSH drop or timeout after the migration commits and before the ledger write leaves the schema migrated with no record of it.
- A failure in the middle of a transaction-less migration leaves half of it applied.
- Two concurrent `central-migrate.sh up` runs both see a version as missing and both apply it.

**Impact:**
1. The next run tries the migration again.
   - A non-idempotent one fails, and the operator now has to work out by hand what is applied.
   - An idempotent one re-runs, which is the M2 problem.
2. On Central, two operators (or a retried deploy) can double-apply a data migration.
3. All of this happens during an upgrade window, when the operator least wants a puzzle.

**Fix:**
1. **Central:** run the migration and its ledger insert in one transaction.
   - Pipe `BEGIN;`, the file, the `INSERT INTO schema_migrations ...` and `COMMIT;` into one psql with `ON_ERROR_STOP`.
   - Make `central-migrate.sh` refuse a *new* file that contains its own `BEGIN`/`COMMIT` (a pre-commit check); existing applied files are never re-run.
   - Take `pg_advisory_lock(hashtext('central-migrate'))` for the whole `up` and `adopt`.
2. **Appliance:** the same shape.
   - The runner wraps the file between `BEGIN;` and its ledger insert plus `COMMIT;`.
   - New migrations must not carry their own transaction control. Add a repository check that refuses `BEGIN;`/`COMMIT;` in a migration numbered above the current head.
   - Migrations that genuinely cannot run in a transaction (`CREATE INDEX CONCURRENTLY`, see L9) get an explicit marker comment, and the runner runs them alone with a separate, clearly reported ledger step.
3. Do not edit the 9 old transaction-less files; they are history.

**Test:**
- A self-test that kills psql between the body and the ledger write (a migration containing `SELECT pg_terminate_backend(pg_backend_pid())` at the end) leaves neither the objects nor the row.
- Two concurrent `central-migrate.sh up` runs apply each version exactly once.

### M4 — A restore replays erasure-ledger entries that nothing authenticates

**Status:** Possible (depends on how `scd` protects the ledger copies; Phase 4) · CONTROLLED (guest data, destructive data)

**Where:** `data-plane/migrations/0148_the_erasure_ledger_is_kept_in_two_places_and_a_restore_reapplies_it.up.sql`:
- `:72-79`: `ledger_hash` is a plain SHA-256 of the entry fields, with no key.
- `:408-470`: `ledger_apply` accepts any entry list whose chain is self-consistent and replays `erase` entries through `ledger_replay_erase` (`:330-362`), which calls `guest_data_erase_core` for each listed record.
- `:419-427`: with `p_adopt = true`, a database that holds only its genesis row deletes it and takes over whichever ledger id the caller supplies.
- `:607-613`: `ledger_apply` is granted to `svc_scd`.

**Problem:**
- The chain proves order, not origin. Anyone can compute a valid continuation of the chain.
- So the database relies entirely on `scd` to bring entries only from its own two copies, and on those copies not being writable by anyone else.
- The adopt path accepts a foreign ledger outright.

**Impact (if the copies are not authenticated):**
1. Someone who can write the secondary ledger destination (a network share, a USB disk) appends `erase` entries naming real guest records.
2. The next restore, or the hourly apply, anonymises those guests. Erasure cannot be undone except from a backup, and the backup replays the same forged ledger.
3. A freshly restored appliance in the genesis-only state adopts a forged ledger wholesale.

**Fix:**
1. In Phase 4, confirm what `scd` does. If the file copies are sealed with an authenticated key (AEAD under a local key), the remaining risk is a compromised `scd`, and this drops to Low.
2. Either way, add a keyed MAC to each entry: an HMAC under a per-appliance key held in the existing local-keys store. `ledger_apply` verifies it through a small definer function that receives the key from `scd`, or `scd` verifies before calling.
3. Allow `p_adopt` only when the supplied genesis entry carries a valid MAC under this appliance's key.

**Test:**
- An entry with a correct chain hash but a wrong MAC is refused, and nothing is erased.
- Adopting a foreign ledger is refused.
- A genuine restore still replays.

### M5 — The OTP and key-bootstrap database tests fail against the current schema, and nothing runs them

**Status:** Confirmed (run on the disposable database)

**Where:**
- `data-plane/internal/otp/otp_keyed_test.go` (`otpDB`, `:18-33`; the fixtures from `:49`) and `otp_device_binding_test.go`.
- `data-plane/internal/keybootstrap/keybootstrap_test.go:156`.
- No script, tool or workflow sets `OTP_TEST_DSN` or `OTPBOOT_TEST_DSN`.

**Problem:**
1. The 7 tests below insert `auth_otps` rows for a tenant that does not exist and with no key-generation row. The current schema requires both (`auth_otps_tenant_id_fkey`, `auth_otps_otp_key_generation_fkey`, `tenant_id NOT NULL`).
   - `TestKeyedIssueThenVerify`
   - `TestKeyedWrongCodeIncrementsAttemptsThenLocks`
   - `TestKeyedConcurrentSingleWinner`
   - `TestLegacyRowVerifiesUnderRing`
   - `TestLegacyIssueUnchangedWhenNoRing`
   - `TestACodeVerifiesOnlyFromTheDeviceItWasIssuedTo`
   - `TestBootstrapOTPReferencedGenMissingKeyFails`
2. They skip silently when the variable is unset, so they have not passed since the schema changed.

**Impact:**
1. The behaviour these tests exist for is unproven on the real schema:
   - one-time-code lockout;
   - single-winner concurrent verification;
   - device binding;
   - the key-generation bootstrap.
2. Phase 2's M11 (code-request race) and the fixes in Phase 2 batch B9 have no working database test to build on.

**Fix:**
1. A shared test helper seeds the minimum rows: tenant, site, and an OTP key generation matching the test ring.
2. Add one script, `scripts/db-integration-tests.sh`. It builds the full schema with `fullschema_build`, sets every `*_TEST_DSN` / `*_DB_URL` variable to a fresh copy, runs the database suites of both modules, and removes the container and its volume.
3. Call it from FULL CHECK (`tools/preflight.sh`). It is not a required merge check (D42).

**Test:** the script runs green on master, and fails when one of these fixtures is broken on purpose.

### M6 — 130 foreign keys have no matching index; 39 of them cascade deletes

**Status:** Possible (needs measurement on realistic volume)

**Where:** catalog query on the upgrade-path build. The list is kept on the audit workstation. Hot examples:
- `iam_v2.entitlements`: 7 foreign keys (stay, purchase, voucher, guest principal, guest account, anonymous subject, superseded entitlement); 4 cascade.
- `iam_v2.stay_guests → stays` (cascade).
- `iam_v2.auth_contexts → devices`, `→ stays`.
- `iam_v2.stays → stay_events` (`last_applied_event_id`), and `stay_links` both ways.
- `iam_v2.device_network_appearances → devices`.
- `public.auth_otps → otp_hmac_key_generations`, `→ appliances`.

**Problem:**
- Deleting or re-keying a parent row, or any join or lookup on these columns, scans the child table.
- Migration 0149 is the proof that this happens here: `stay_events` had no index on `stay_id`, and the hourly retention run timed out on PRE-LIVE with 374,000 events and anonymised nothing.

**Impact:**
1. Retention and anonymisation runs (0127, 0135, 0147), which delete and update guest records in bulk, slow down or hit the 30 s statement timeout as tables grow.
2. They then silently do nothing, which is the GDPR failure 0149 fixed for one table.
3. Cascading deletes hold locks for the whole scan.

**Fix:**
1. Build a synthetic dataset at a year's volume for a large hotel. It is synthetic only: never PRE-LIVE's guest list.
2. `EXPLAIN (ANALYZE, BUFFERS)` the retention, anonymisation, erasure and PMS-resync functions on it.
3. Add the indexes that matter in one new migration, using `CREATE INDEX CONCURRENTLY` (see L9).
4. Skip foreign keys whose lookups an existing partial index already serves, such as 0149's `stay_events_by_stay`.

**Test:** an EXPLAIN-based test, on the synthetic dataset, that the retention batch and an erasure use index scans and finish inside the statement timeout.

## Low

| # | Problem | Where | Recommended fix |
|---|---|---|---|
| L1 | Seven guard trigger functions read `pg_class`, `pg_proc` or `pg_roles` unqualified, with no pinned `search_path`. A temp table named `pg_class` shadows the catalog. Reproduced as `svc_edged`: the owner check then returns true. **Not exploitable today:** no runtime role holds the table privilege each guard protects, except `svc_edged` on `guest_access_accounts`, where a bypass only allows renaming a removed account to another removed-style name. One wrong grant in future would open the others. | `accounting_records_append_only` (0122), `device_slot_actions_append_only` (0140), `pms_case_resolutions_append_only` (0140), `trg_entitlement_guard` (0124), `guest_data_erasures_append_only` (0143), `guest_access_account_removed_is_final` (0127:616), `trg_catalogue_revision_immutable` (0091) | A new migration re-creates them with `SET search_path = pg_catalog, pg_temp` and schema-qualified names. Add a catalog test: every trigger function in `iam_v2` has a pinned `search_path`. |
| L2 | `central_forget_actor` lowercases the email but matches audit payloads with case-sensitive `LIKE`, so an email stored in its original case survives. It also scans the whole `audit_log` inside the operator `DELETE` transaction. | `control-plane/migrations/0055_*.up.sql:22`, `:42-44` | Match with `lower(payload::text) LIKE` and replace case-insensitively. Run the scrub in batches outside the delete transaction. Do it with M1, as the definer function. |
| L3 | Guest erasure rewrites `audit_log` across the whole tenant, from `-infinity` when the guest's first record is unknown, using `LIKE ANY` on every payload. This runs inside the erasure transaction, which holds the ledger advisory lock and so serialises every erasure. | `data-plane/migrations/0147_*.up.sql:960-1005` | Bound by site where `audit_log` has it. Batch the update. Measure with M6's dataset. |
| L4 | `central-migrate.sh` puts the version, checksum and user name straight into SQL strings, and `--through` is not validated. `adopt --through 9999 --yes` records every migration as applied without running it. | `deploy/scripts/central-migrate.sh:132`, `:138`, `:178`, `:248-261` | Validate `^[0-9]{4}_[a-z0-9_]+$` for versions and `^[0-9]{4}$` for `--through`, as `edge-migrate.sh:133` does. Pass values with `psql -v` and `:'var'`. |
| L5 | Central migrations 0040 and 0041 insert their own ledger row with no checksum. If the runner's own insert is then lost (M3), the row counts as a pre-ledger row and its checksum is backfilled as `adopted`, which weakens the tamper evidence for exactly those two. | `0040_simple_license_model.up.sql:50`, `0041_license_per_appliance.up.sql:32`, `0040...down.sql:11` | Do not edit them (applied history). After M3, the runner's insert in the same transaction overwrites the row with its checksum and `adopted=false`. Add a rule: a migration never writes `schema_migrations`. |
| L6 | The appliance ledger stores only the version, no checksum. A migration file edited after it was applied goes unnoticed on the appliance. Central detects the same thing. | `scripts/edge-migrate.sh:613`; the ledger shape at `:432-440` | Add a `checksum` column (nullable, through `--bootstrap-ledger`'s successor). The runner writes it and `status` reports a mismatch. |
| L7 | The baseline's last covered migration is written in three places that nothing checks against each other: the baseline header, `lib-fullschema-testdb.sh:24` and `sitedb-dev.sh:20`. A regenerated baseline with an un-updated constant re-applies or skips a migration in the test harnesses. | as listed | One function reads it from the header (part of M2). |
| L8 | `ledger_confirm` marks entries durable for any hash passed in, without proof that both copies exist. It is granted to `svc_edged`, which never writes the ledger. | `0148_*.up.sql:136-146`, `:603` | Revoke it from `svc_edged`. Do it with M4. |
| L9 | Index builds on large tables run inside a transaction, so they block writes for the whole build. This is acceptable at 374,000 rows (0149 measured seconds), but not on a bigger hotel. | `0149_*.up.sql:13-16` and the pattern in general | For indexes on tables that grow with guests or events, use `CREATE INDEX CONCURRENTLY` as a runner-recognised single-statement migration (M3, fix 2). |
| L10 | Deleting a Central appliance row cascades away its certificate history, lifecycle events, offline activation requests and portal-certificate records. | `0022:14,23`, `0023:16,29,53`, `0044:15`, `0050:33,51,73` | **DECISION:** is that history audit evidence to keep? If yes, change the keys to `RESTRICT` and soft-delete appliances. Decide it together with Phase 1 M8 (appliance deletion is not atomic). |
| L11 | No table in `iam_v2` uses row-level security. Tenant and site isolation rests on every query and definer function filtering by `tenant_id`/`site_id`. This is acceptable for a single-hotel appliance, but it is not enforced anywhere. | 0 of 161 `iam_v2` tables have RLS | No RLS now. Add a catalog test that every definer function taking `p_tenant`/`p_site` uses them; review per function in Phases 4–9. |
| L12 | Nothing checks that a shipped appliance binary was built with the production profile, without `-tags devlicense`. | `data-plane/internal/buildprofile/*.go` | Part of Phase 0 P0-3: the build script refuses `devlicense`, and the release check reads `go version -m` for the tags. |

## Clean code

| # | Problem | Recommended fix |
|---|---|---|
| CC1 | Both runners and the build scripts are mostly narrative history: incidents, increments, reviewer quotes. `edge-migrate.sh` is 775 lines, most of them comments. This contradicts the project's "no comments unless critical" rule and hides the logic. | Keep one-line reasons in the code. Move the history into `docs/` (the runbook) when M2/M3 touch these files. |
| CC2 | The container-readiness loop (wait for the second "ready to accept connections", then three clean queries) is copied in `lib-fullschema-testdb.sh`, `factory-clean-baseline-verify.sh` and `clean-install-reconstruction.sh`. | One function in `lib-fullschema-testdb.sh`; the others source it. |
| CC3 | Two runners with two ledger conventions. Central keys by name and stores checksum, applier and adoption. The appliance stores the version only and checks the checksum only at the caller. | Converge on Central's ledger columns when doing L6; keep the appliance's stronger target-identity checks. |
| CC4 | `central-migrate.sh status` always prints "0 awaiting checksum backfill", because `${nocs:+...}` tests for non-empty and `0` is non-empty. | Print it only when `nocs > 0`. |

## Fix batches

A batch is done when its tests pass and both modules' affected tests still pass. Every schema batch is a **new** migration; no applied file is edited.

| Batch | Findings | Files | Area and notes |
|---|---|---|---|
| **B1** Database integration harness | M5 | `scripts/db-integration-tests.sh` (new), `data-plane/internal/otp/*_test.go`, `keybootstrap/keybootstrap_test.go`, `tools/preflight.sh` | No product code. **Do first:** it is the harness the other batches, and Phase 2 B8/B9, test with. |
| **B2** Appliance runner and baseline ledger | M2, M3 (appliance part), L6, L7, CC3 (appliance part) | `scripts/edge-migrate.sh`, `edge-migrate-selftest.sh`, `generate-production-baseline.sh`, `factory-clean-baseline-verify.sh`, `lib-fullschema-testdb.sh`, `sitedb-dev.sh`, regenerated `baseline/0000_production_baseline.sql` | CONTROLLED (migration, deployment). The one-time adopt on PRE-LIVE is a deployment step. |
| **B3** Central runner | M3 (Central part), L4, L5, CC4 | `deploy/scripts/central-migrate.sh`, `central-scripts-selftest.sh` | CONTROLLED (migration). |
| **B4** Central database least privilege | M1, L2 | new Central migration, `deploy/scripts/central-install.sh`, `deploy/env/ctrlapi.env.example`, `control-plane/internal/testdb` | CONTROLLED (schema, deployment). Run after B3 (it uses the corrected runner). Touches Central deployment: coordinate with Phase 1 batches that change `central-install.sh`. |
| **B5** Erasure ledger authentication | M4, L8, L3 | new appliance migration; `cmd/scd` ledger code | CONTROLLED (guest data). Start after the Phase 4 check of `scd`'s copy handling. |
| **B6** Guard functions | L1 | new appliance migration; a catalog test in `data-plane/internal/iamv2` | CONTROLLED (guest authentication core). Small. |
| **B7** Indexes for retention and erasure | M6, L9 | new appliance migration(s); synthetic-volume test data | Needs B1 and B2 (concurrent-index migrations). |
| **B8** Clean code | CC1, CC2 | the runners and build scripts | With or after B2/B3, never before (same files). |

**Waiting for a decision**

| Item | Decision | Recommended |
|---|---|---|
| L10 | Keep a Central appliance's certificate and lifecycle history after the appliance is deleted? | Yes: `RESTRICT` and soft delete. |
| M2, fix 4 | Record 0001–0149 as applied on existing baseline-built appliances (PRE-LIVE) with a one-time audited adopt step? | Yes, as part of B2's deployment. |

## Checked and rejected

- **"Central's retention deletes licences that rollback protection depends on."** 0056 (`:145-165`) never removes the newest licence of an existing appliance, and keeps a revoked licence until its validity and the history period have both ended.
- **"`edge-migrate.sh` loses the psql exit code under `set -e`."** `apply_one` and `revert_one` are always called between `set +e` and `set -e` (`:743`, `:764`, `:770`), so `rc` is captured.
- **"`central-migrate.sh`'s `BEGIN` detection matches comments."** The pattern is anchored at the start of the line (`^[[:space:]]*BEGIN`), and comment lines start with `--`.
- **"0147's `erasure_journal_replay` accepts any journal."** 0148 replaces that path, and an appliance cannot stop between the two in normal deployment. What remains is M4.
- **"Reverting 0133 alone reintroduces the 0135 GDPR gap."** The runner only reverts the head of the ledger, so 0133 cannot be reverted while 0135 is applied.
- **"Service roles can delete attempt and event tables."** The `DELETE` grants (`sign_in_attempts`, `pms_attempts`, `stripe_events`, `appliance_recovery_events`) are retention purges. The `UPDATE` grants on `stay_events` and `posting_attempts` are blocked by their one-way triggers.

## Not verified

- **The crash windows in M3.** Traced in the code, not reproduced by killing psql.
- **Volume behaviour (M6, L3, L9).** Needs the synthetic dataset.
- **The `scd` side of M4.** Phase 4.
- **The service-role suites.** The suites that need a real service-role login (`EDGED_ROLE_TEST_DSN`, `SCD_ROLE_TEST_DSN`, `SOCIAL_TEST_SCD_DSN`, `ROOMCHARGE_*`, `PORTALTLS_PG_URL`) were not run here; they belong to Phases 4, 5, 6 and 9, which can use B1's harness.
- **Phase 2's writer guard (M10) and code-request race (M11).** Still not proven on a database. M11's suite is the broken one in M5.
- **Race-detector runs.** These still need a Linux build host.
- **Appliance migrations 0001–0119.** Covered by the catalog checks and the drift check, not read line by line.
