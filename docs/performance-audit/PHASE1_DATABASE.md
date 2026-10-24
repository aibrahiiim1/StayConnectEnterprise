# Phase 1: Database foundations

**Audited:** master `653363d7`. Code and repository only: nothing was run against PRE-LIVE.

**Covers:**
- the Postgres settings the appliance actually gets;
- every connection pool, and the per-role connection limits;
- which tables grow, how fast, and what removes old rows;
- index use for the queries that run every second;
- the items Phase 0 handed on (H1, H3, H4, and H5 in part).

**How it was measured:**
- **Settings:** the appliance's TimescaleDB image was started three ways and the settings it chose were read back. The three runs were limited to 4 GB, unlimited on the workstation, and told it had exactly 2 CPUs and 4 GB.
- **Data:** a throwaway full-schema database was built with `scripts/lib-fullschema-testdb.sh`, limited to 2 CPUs and 4 GB and given the reference settings. It was filled with generated data only:
  - 732,000 sessions, one year at 2,000 a day, of which 2,000 are live;
  - their entitlements and bindings;
  - 10 million accounting rows.
- **Calls:** the real database functions were called as acctd calls them, one transaction per call.

**Caveat:** the workstation's disk and fsync are not the appliance's. Times are for comparison and for growth curves. Phase 11 lists what to repeat on a reference VM.

## Summary

| Critical | High | Medium | Low | Clean code |
|---|---|---|---|---|
| 0 | 3 | 3 | 7 | 0 |

**Main result.** The traffic-accounting path cannot keep up at the design load, for two reasons:
- Every controlled database operation scans an hour of bookkeeping rows with no index (D1-H1).
- acctd records every online device every second, one transaction per device (D1-H2).

Together they hold a pass over 2,000 devices at about **30 seconds instead of 1**. Allowances and time limits are enforced that much later.

The detail rows it writes also grow faster than anything removes them. A 40 GB disk fills in weeks now, and in days once the speed is fixed (D1-H3).

Two of the three Highs have a one-line index as their first fix.

## Findings

### D1-H1: Every controlled operation scans an hour of bookkeeping rows (High, Measured)

**Where:**
- `iam_v2.begin_controlled_operation` runs `DELETE FROM iam_v2.controlled_operation_scope WHERE opened_at < now() - interval '1 hour'` on every call. It is the "bounded janitor".
- The table's only index is its primary key, `(txid, family)`.
- 18 database functions reference it, across its 10 families: sign-in resolution, stays, the commerce intent, device authorization, session binding, accounting and others.

**Problem.**
- A row is inserted per operation and is never removed when the operation commits. So the table always holds every operation of the last hour.
- With no index on `opened_at`, the janitor reads the whole table on every operation.

**Measured** on the reference database:

| Rows in the table | Janitor time per operation |
|---|---|
| 8,000 | 0.35 ms |
| 1,000,000 (about an hour of accounting) | **48 ms** (sequential scan of 1,008,006 rows) |
| 1,000,000, with an index on `opened_at` | **0.04 ms** |

The full accounting call went from about 3.3 ms to **51 ms** with the million-row table: 200 calls took 10.2 s.

**Impact.**
- **The cost feeds itself.** The more operations run, the slower each one gets.
- **Accounting settles at about 67 calls a second.** Solving for that balance, using the measured costs, gives about 15 ms per call. A pass over 2,000 devices then takes about 30 s.
- **Every guest sign-in pays the same scan**, because sign-ins use the same controlled operations. Phase 2 measures it.

**Recommended fix.**
1. **Now:** add `CREATE INDEX ON iam_v2.controlled_operation_scope (opened_at)`.
2. **Then:** stop paying the janitor per operation. Either delete the operation's own row when it ends, or run the janitor from one periodic job, for example scd's hourly retention loop.

Both are a migration.

**Proof.**
- `EXPLAIN` of the janitor shows an index scan.
- With a million rows present, the accounting call stays under 5 ms.
- The Phase 2 sign-in storm shows no growth in time per sign-in over an hour.

**Batch:** B1. CONTROLLED (database schema; the guest enforcement core).

### D1-H2: acctd records every online device every second, one transaction each (High, Measured)

**Where:**
- **The tick:** `cmd/acctd/main.go:181`. It runs every 1 s by default (`ACCTD_TICK_SECONDS`, clamped to 1–10, unset in `provision-fresh-appliance.sh:347`).
- **The pass:** `cmd/acctd/phase3_accounting.go:76-152`.
- **The call:** for each live session, one `SELECT iam_v2.ingest_absolute_counters(...)`, which is its own transaction (`phase3_accounting.go:161`).

**Problem.** One call per device per second, run one after another on one connection. Each call costs:
- about a dozen statements;
- 3 to 4 trigger-guarded updates: the session, the entitlement and the checkpoint;
- the insert of an accounting row;
- the controlled-operation bookkeeping (D1-H1);
- a commit with an fsync.

**Measured** for one pass over 2,000 live devices, without the D1-H1 growth:

| How it was run | Time for 2,000 devices |
|---|---|
| All calls inside one transaction (lower bound) | 4.8 s |
| As acctd runs it: one transaction per call | **6.4–6.9 s** (about 3.3 ms per device) |

**Where the database time goes,** per 2,000 calls (`pg_stat_statements`, nested statements included):

| Statement | Time |
|---|---|
| Updating the session's byte totals | 1.2 s |
| Inserting the accounting row | 1.0 s |
| `begin_controlled_operation` | 1.0 s (0.7 s of it is the janitor) |
| Updating the entitlement | 0.4 s |
| Everything else | < 0.4 s |

The rest of the 6.5 s is commits and round trips.

**Impact.**
- At about 3 ms a device, one second fits about 300 devices; at the design load the pass already takes six times its tick.
- With D1-H1's growth, a pass takes about 30 s.
- The loop runs back to back and keeps one of the reference appliance's two CPUs busy in Postgres all the time.
- Everything acctd does after accounting waits for the pass: session limits, expiry, the shaping plan. So a guest's data allowance, time limit and expiry are enforced tens of seconds late.

**Recommended fix.** Phase 5 designs this in detail with the rest of acctd's loop. In outline:
1. **Batch the pass.** One call per tick takes arrays of (session, device, bridge, class, epoch, counters) and does the same checks for all of them in one transaction, as set-based statements. That removes 1,999 commits and round trips.
2. **Make the tick a setting** (CLAUDE.md §0C), measured against the precision allowances need. **DECISION:** how quickly a used-up allowance must cut a guest off. The recommended default is 5 s.
3. **Skip devices whose counters have not moved.** acctd already reads the counters before calling, so it can drop them without a database call.

**Proof.**
- A pass over 2,000 devices, as acctd runs it, completes in under 500 ms on the reference database.
- The Phase 5 load run shows Postgres CPU below 25% at design load.

**Batch:** B2, with Phase 5. CONTROLLED (accounting is part of guest enforcement).

### D1-H3: Accounting detail grows faster than anything removes it (High, Measured)

**Where:**
- **Rows:** `iam_v2.accounting_records`. It is a plain table, not a hypertable, and gets one row per device per pass whenever the device's counters moved (`ingest_absolute_counters`).
- **Removal:** only `iam_v2.purge_accounting_detail`, called hourly by acctd (`cmd/acctd/phase3.go:121-146`). It removes at most 5,000 × 20 = **100,000 rows an hour**.
- **What it may remove:** only rows older than the retention, 365 days by default (migration 0131), of sessions that ended before it.

**Measured.**
- **Row size:** 245 bytes per row including its two indexes, freshly loaded with no bloat. 10 million rows take 1.28 GB of table and 1.06 GB of index.
- **The purge itself is cheap:** a 5,000-row batch took 0.36–0.41 s.

**Growth at the design load** (2,000 devices). Rows per day = devices × the share whose counters move each pass × 86,400 ÷ seconds per pass.

| Situation | Rows a day | Disk a day |
|---|---|---|
| Today, held back by D1-H1 (about 67 rows a second) | 5.8 million | 1.4 GB |
| D1-H1 and D1-H2 fixed, 1 s tick, 10% of devices moving each second | 17 million | 4.2 GB |
| Same, 30% moving | 52 million | 12.7 GB |
| Same, 5 s tick, 30% moving | 10 million | 2.5 GB |
| Purge capacity, once rows are a year old | 2.4 million | 0.6 GB |

These numbers leave out WAL, backups and bloat, which add to them.

**Impact.**
- **For the first year, nothing is removed at all.** No row is old enough.
- **After that, the purge's hourly cap is below the insert rate in every row of the table above.**
- **On the reference 40 GB disk, Postgres fills it.** That takes about two to three weeks at today's rate, and days once D1-H2 is fixed. Postgres then stops accepting writes, and guest sign-in, accounting and the Admin Console stop with it.
- **Backups grow at the same rate** (Phase 10).
- **Fixing D1-H2 alone makes this worse.** The two must ship together.

**Recommended fix.**
1. **Keep detail at a coarser grain.** Store one row per session per minute instead of per second, or keep per-pass rows only for a short window and roll them up into hourly totals kept for the full retention. The totals on the session and entitlement are already maintained separately and do not need the detail. **DECISION:** what usage detail the hotel needs to keep, at what grain and for how long. GDPR plan C2 set 12 months for "usage detail" without a grain.
2. **Make `iam_v2.accounting_records`** (or the roll-up table) a hypertable on `sampled_at`, with chunks of a day, and remove old detail by dropping whole chunks instead of deleting rows. The "session ended and every entitlement terminated" rule moves to the roll-up step.
3. Until then, make the purge's hourly cap follow the insert rate instead of a fixed 100,000.

**Proof.**
- A 30-day run of the generator at the design load stays under a stated disk budget, for example 2 GB for accounting.
- Removing old detail is a chunk drop that takes under a second.

**Batch:** B3. **DECISION** (detail grain and retention); CONTROLLED (database schema; historical data).

### D1-M1: Finding the live sessions reads every session ever recorded (Medium, Measured)

**Where:**
- **The query:** `cmd/acctd/phase3_accounting.go:44-48`, run every tick: `WHERE tenant_id=$1 AND site_id=$2 AND state='active' AND ended IS NULL ORDER BY started`.
- **Similar filters** on `state = 'active'` appear in edged's session and device pages (`resources_sessions.go:92,243-246`, `client_devices_admin.go:286`), which Phase 7 measures.
- **Indexes:** `iam_v2.sessions` has none on `state` or `ended`. Sessions are never deleted, only anonymised.

**Measured** with one year of sessions (732,000 rows, 128 MB):

| | Time |
|---|---|
| Today, warm cache | 38–40 ms. A parallel sequential scan that discards 730,000 rows, and the planner expects 3 rows where there are 2,000. |
| Today, cold (first run after the load) | 8.9 s |
| With a partial index | **1.4 ms** |

The partial index was: `CREATE INDEX ON iam_v2.sessions (tenant_id, site_id, started) WHERE state='active' AND ended IS NULL`.

**Impact.**
- About 4% of the reference appliance's CPU, every second, for this one query, before any work is done.
- It grows with every session ever recorded: about 40 ms more each year.

**Recommended fix.**
- Add the partial index above.
- Phases 2 and 7 check their own session queries against it.

**Proof.** `EXPLAIN` shows the index scan, and the query takes under 5 ms with three years of sessions.

**Batch:** B1. CONTROLLED (database schema).

### D1-M2: Postgres is tuned as if it owned the whole appliance (Medium, Measured)

**Where:**
- **Compose file:** `deploy/compose/infra.yml:12-24`. It sets no memory limit and no tuning, only two log settings.
- **Tuning:** the TimescaleDB image runs `timescaledb-tune` once, at first start, against the memory and CPUs it sees.

**Measured,** what it chose for exactly 2 CPUs and 4 GB, which is the reference appliance:

| Setting | Value |
|---|---|
| `shared_buffers` | 1.25 GB |
| `effective_cache_size` | 3.75 GB, which is all of the machine's memory |
| `maintenance_work_mem` | 512 MB |
| `autovacuum_max_workers` | 10 |
| `work_mem` | 10 MB |
| `max_connections` | 50 |
| `timescaledb.max_background_workers` | 16 |

It assumes Postgres is alone on the machine. On the appliance, Postgres shares the box with:
- scd, edged, portald, netd, acctd and pmsd;
- Node for hotel-admin (capped at 768 MB);
- Redis, Caddy, Kea and Unbound.

**Impact** (Suspected; Phase 10 measures the budget).
- **The planner believes 3.75 GB of cache exists** that the other services are using.
- **Ten autovacuum workers can each take up to 512 MB.** Two or three vacuuming the accounting tables at once can take 1–1.5 GB on top of the shared buffers.
- **The settings are frozen at first start.** An appliance initialised on a bigger VM, or before a resize, keeps the old numbers.

When memory runs out, the kernel kills whichever process it picks, and nothing in the deploy files says which.

**Recommended fix.**
1. Put explicit settings in the compose file (or a mounted config), sized from Phase 10's budget. A starting point for 4 GB:

   | Setting | Starting value |
   |---|---|
   | `shared_buffers` | 768 MB–1 GB |
   | `effective_cache_size` | 2 GB |
   | `maintenance_work_mem` | 256 MB |
   | `autovacuum_work_mem` | 128 MB |
   | `autovacuum_max_workers` | 3 |
   | `timescaledb.max_background_workers` | 4 |

2. Give the container a memory limit.
3. Give the services an out-of-memory priority, so Postgres and scd are the last to go.

**Proof.** Phase 10's load run at the design load shows total memory under the budget with 15% headroom, and no swap.

**Batch:** B4, with Phase 10. CONTROLLED (appliance deployment).

### D1-M3: No service chooses its pool size; every pool has 4 connections (Medium, Confirmed)

**Where:**
- **Pools:** every appliance `pgxpool.New` uses pgx's default size, `max(4, CPUs)`, which is 4 on the reference appliance:
  - acctd `main.go:125`;
  - edged `main.go:152`;
  - netd `main.go:63`;
  - pmsd `main.go:49`, `internal/pmsd/pg.go:24`;
  - scd `main.go:543`, `card_checkout.go:81,86`, `posting_worker.go:48`;
  - `internal/payment/engine.go:53`.
- **Connection strings:** `deploy/gatep/gatep-set-passwords.sh:39` writes them without `pool_max_conns`.
- **Central** sets 10 (`control-plane/internal/db/db.go:15`).

**Problem.** scd's main pool carries, on 4 connections:
- every guest sign-in;
- the portal's lookups through scd;
- OTP;
- PMS validation;
- the reaper;
- the retention, backup and assignment loops.

A query that waits on a lock, or a retention batch of up to 2 minutes (`data_retention.go:111`), holds one of the four.

**Impact** (Suspected). In a sign-in storm, requests may queue for a connection rather than for the database. Phase 2 measures the pool wait.

**Recommended fix.**
- Give each service an explicit pool size as configuration, with a default per service. For example: scd 12, edged 8, acctd 3, netd 3, pmsd 3, payment and posting 2 each.
- Set the connection lifetime and idle time explicitly.
- Keep the sum within `max_connections` (D1-L1).
- Export pool wait time as a metric (Phase 0 B0-1).

**Proof.** In the Phase 2 storm, the pool's acquire wait p95 stays under 5 ms.

**Batch:** B5.

### D1-L1: The per-role connection limits add up to more than Postgres allows (Low, Confirmed)

**Where:** `deploy/gatep/gatep-roles.sql:31-38`:
- scd 20, edged 20;
- acctd, netd and pmsd 10 each;
- payment, payment outcome and posting 5 each.

**Problem.**
- The limits add up to 85 connections.
- `max_connections` is 50, of which 3 are reserved for superusers, so 47 are usable.
- The TimescaleDB background workers and any `pg_dump` also take slots.

**Impact.**
- Today the pools use only about 32, so nothing fails.
- Once D1-M3 raises pool sizes up to the role limits, the services together can exhaust Postgres. The service that loses is whichever connects last, often scd after a restart.

**Recommended fix.** Size the role limits and `max_connections` together:
- each role's limit is the service's pool size plus 2;
- the sum plus the background workers stays below `max_connections`.

**Batch:** B5.

### D1-L2: The database has no guard rails or slow-query evidence (Low, Measured)

**Where:**
- `statement_timeout` and `idle_in_transaction_session_timeout` are both 0.
- `log_min_duration_statement` is off.
- `pg_stat_statements` is installed but not loaded.
- `track_io_timing` is off.

**Impact.**
- A stuck query or an abandoned transaction holds locks and a pool connection until someone restarts the service.
- A slow query on PRE-LIVE leaves no trace.

**Recommended fix.**
1. **Timeouts per role:** a statement timeout for each (for example scd 5 s, edged 30 s, the retention and backup paths their own), and an idle-in-transaction timeout of 60 s.
2. **Statistics:** load `pg_stat_statements`.
3. **Slow-query log:** set `log_min_duration_statement` to 500 ms.

**Guest data:** the slow-query log must not print bound parameters, which can be guest data. Set `log_parameter_max_length = 0` and `log_parameter_max_length_on_error = 0`, in line with the existing `log_min_error_statement=panic` rule (`infra.yml:22-24`).

**Batch:** B4. CONTROLLED (appliance deployment).

### D1-L3: The rows updated every second leave half their updates off the fast path (Low, Measured)

**Where:** `iam_v2.sessions`, `iam_v2.entitlements` and `iam_v2.accounting_checkpoints`. They are updated on every accounting call, with the default fill factor of 100.

**Measured** after the test passes: about 45% of updates were "HOT" on all three tables (sessions 8,210 of 22,205; entitlements 8,156 of 18,205; checkpoints 8,273 of 18,205). Every other update writes a new entry into every index. `entitlements` has 10 indexes.

**Impact.**
- More writes, more WAL and more vacuum work on the busiest rows.
- This is small next to D1-H2, but it scales with it.

**Recommended fix.** Set `fillfactor = 85` on the three tables and rebuild them in a maintenance window.

**Proof.** The HOT share rises above 90% in the B2 load run.

**Batch:** B6. CONTROLLED (database schema).

### D1-L4: The hypertable for accounting is the one nothing writes any more (Low, Confirmed)

**Where:**
- `public.accounting_records` is a hypertable with one-day chunks. No Go code reads or writes it.
- The live table, `iam_v2.accounting_records`, is a plain table.

**Impact.**
- The table that grows (D1-H3) gets none of TimescaleDB's chunk dropping.
- An unused table and its three indexes remain in every schema and backup.

**Recommended fix.**
- Fold into D1-H3: the live detail becomes the hypertable.
- Retire `public.accounting_records` after confirming on PRE-LIVE that it is empty or unused (read-only check).

**Batch:** B3. CONTROLLED.

### D1-L5: Several per-event tables are never trimmed (Low, Confirmed)

**Where.** No `DELETE` or chunk drop exists, in any migration or in the Go code, for:
- `public.audit_log` (a hypertable with 7-day chunks);
- `public.auth_otps`, `public.pms_attempts`;
- `iam_v2.device_network_appearances`, `iam_v2.auth_contexts`, `iam_v2.auth_resolutions`;
- `iam_v2.accounting_checkpoints`, one row per session ever;
- `iam_v2.session_entitlement_bindings`, `iam_v2.entitlement_state_transitions`;
- `iam_v2.sessions`.

Retention anonymises guest data in these tables in place (`cmd/scd/data_retention.go`); it does not remove rows.

**Measured.** One year of sessions is 128 MB. The other per-session tables are of the same order.

**Impact.**
- Small at the design load: hundreds of MB a year in total.
- But every query on them without a fitting index gets slower each year (D1-M1 is the example).
- `audit_log` grows with staff activity, and would grow faster if guest-list views are audited (backend audit P9 M4).

**Recommended fix.**
- Decide per table whether an anonymised row has any further use.
- Where it does not, delete it in the existing retention loop.
- For `audit_log`, drop chunks older than the activity-log setting.
- Phases 2–7 confirm each of their queries on these tables has an index that does not degrade with age.

**Batch:** B7. CONTROLLED (historical data).

### D1-L6: edged builds a payment engine, and could open a pool, on every financial request (Low, Confirmed)

**Where:**
- `cmd/edged/resources_phase4_finops.go:422`: `paymentEngine()` calls `payment.NewProductionEngine` per request (6 call sites).
- `internal/payment/engine.go:52-58`: when `STAYCONNECT_PHASE4_PAYMENT_OUTCOME_DSN` is set, that opens a new `pgxpool`. Nothing ever closes it.

**Problem.** Today the variable is written only to `scd.env` (`provision-fresh-appliance.sh:331`), so edged opens no pool. If it is ever added to `edged.env`, each financial request leaks:
- a pool;
- its health-check goroutine;
- up to 4 connections, for 30 minutes.

**Recommended fix.** Build the engine once at edged's start and reuse it, as scd does with its pools.

**Batch:** B5.

### D1-L7: The accounting purge's hourly cap is fixed in code (Low, Confirmed)

**Where:** `cmd/acctd/phase3.go:121-123`: 5,000 rows × 20 batches, once an hour.

**Problem.**
- The cap does not follow the insert rate (D1-H3).
- It is an operational limit set in code, against CLAUDE.md §0C.

**Recommended fix.** Superseded by the chunk drop in D1-H3. Until then, keep purging in batches while a batch returns full, up to a time budget, rather than a fixed count.

**Batch:** B3.

## Items from Phase 0

| Phase 0 item | Outcome here |
|---|---|
| H1, pool sizes | Confirmed: 4 connections per pool. D1-M3 and D1-L1. Pool wait is measured in Phase 2. |
| H3, Postgres memory | Confirmed and measured: D1-M2. The budget is in Phase 10. |
| H4, hypertables and retention | Confirmed: the live accounting table is not a hypertable, and its purge cannot keep up (D1-H3, D1-L4). Other tables are anonymised, not trimmed (D1-L5). |
| H5, no batching | Measured for accounting: D1-H2. The roster load and the other loops are in Phases 5 and 6. |

## Handed on

| To | What |
|---|---|
| Phase 2 | The D1-H1 janitor scan on every sign-in; pool wait time in the storm; sign-in queries on `sessions` and `auth_contexts` against D1-M1's index. |
| Phase 5 | The design of the batched accounting pass (D1-H2); the cost of acctd's other per-tick steps (session limits, expiry, `aggOwner.sweep`, shaping reconcile). |
| Phase 7 | Reports and the Overview that read `iam_v2.accounting_records` (`entitlement_usage_bytes`, the per-session subqueries at baseline lines 1177 and 3344) on a year of detail. |
| Phase 10 | Memory budget for D1-M2; disk budget for D1-H3, including WAL and backups. |
| Phase 11 | Repeat D1-H1, D1-H2 and D1-M1 on a reference VM with the appliance's disk. |

## Batches

| Batch | Findings | Notes |
|---|---|---|
| **B1** | D1-H1 (the index first), D1-M1 | Two indexes in one migration. Small, and it removes the self-feeding slowdown. **Do first.** CONTROLLED. |
| **B2** | D1-H2 | With Phase 5's acctd design. Ships together with B3. CONTROLLED. |
| **B3** | D1-H3, D1-L4, D1-L7 | After the detail-grain decision. CONTROLLED. |
| **B4** | D1-M2, D1-L2 | Deployment configuration, with Phase 10. CONTROLLED. |
| **B5** | D1-M3, D1-L1, D1-L6 | Pool sizes as configuration, role limits, edged's engine. |
| **B6** | D1-L3 | Fill factor. CONTROLLED. |
| **B7** | D1-L5 | Per-table trimming, after the decisions on what anonymised rows are for. CONTROLLED. |

## Decisions for the Product Owner

| # | Decision | Recommended |
|---|---|---|
| D1-H2 | How soon must a used-up allowance or time limit cut a guest off? This sets acctd's tick. | Within 5 s by default, as a setting. |
| D1-H3 | What usage detail is kept, at what grain, for how long? | Per-minute detail for 30 days, hourly totals for the 12-month retention. |

## Tests run

| Test | Result |
|---|---|
| TimescaleDB auto-tuning: 4 GB limit; unlimited (7.6 GB, 16 CPUs); exactly 2 CPUs and 4 GB | Settings recorded in D1-M2 |
| Full-schema throwaway database (migrations to 0150), 2 CPUs, 4 GB, reference settings, generated data | Built |
| Live-session query: with and without the partial index | D1-M1 |
| Accounting pass, 2,000 devices: in one transaction; as separate transactions (×3); with a million bookkeeping rows | D1-H1, D1-H2 |
| Janitor `DELETE`: with and without an `opened_at` index | D1-H1 |
| Purge selection and two real 5,000-row purge batches on 10 million rows | D1-H3 |
| Row size of the accounting table | 245 bytes per row |
| HOT update share after the passes | D1-L3 |

**Cleanup.** The four containers were removed with their volumes (`docker rm -f -v`): three for the tuning runs, one for the test database. The scratch files were deleted. Docker is back to its state before the phase, with 16 volumes. The TimescaleDB image was already present and was kept.
