# PR #272 review: the accounting usage roll-up (2026-10-29)

Scope: `iam_v2.rollup_accounting_usage` / `purge_accounting_detail` (migration 0175), `Enforcer.RollupAccountingUsage`
(`data-plane/internal/enforce/plan_limits.go`), `accountingMaintenance` (`data-plane/cmd/acctd/phase3.go`).
Environment: a disposable local `timescale/timescaledb:2.16.1-pg16` container (2 GB, 2 CPUs, full appliance schema built
by `scripts/lib-fullschema-testdb.sh`, Postgres 16.3), rows loaded with `session_replication_role = replica`, roll-up
and purge run as `SET ROLE svc_acctd` with `statement_timeout='30s'`, `lock_timeout='5s'`. No shared host was contacted.
Timings are from a 2-CPU Docker Desktop box, not the 1 vCPU appliance: read them as ratios.

## Result in one table

| # | Question | Answer | Demonstrated |
|---|---|---|---|
| 1a | An empty gap stalls catch-up? | No. The function jumps to the first sample at or after the watermark; `0` means "nothing below the cap" | yes |
| 1b | A 2-hour window can exceed 30 s? | **Yes**. Poison window: it rolls back whole and is retried unchanged forever | **yes, with the real 30 s timeout** |
| 2 | Atomic, no double count, no loss? | Yes | yes (0 mismatching sessions) |
| 3 | Tick independent of roll-up/purge? | Yes (goroutine + flag; no lock shared with the insert) | yes |
| 4 | A late-committing sample can be skipped, then purged? | Mechanism yes; reachable on the production write path only if a clock steps back or the role timeout is raised | mechanism yes, production path no |
| 5 | Entitlement/session totals and 0122 counters touched? | No writes. But exact re-readers of raw samples shrink once raw is purged | yes |

## 1. Large backlogs and the window

Catch-up. The function does `min(ingested_at) >= watermark` first, so a gap is skipped inside one call (site with samples
10 days, 1 day and 3 hours ago, 2 h window: call 1 reads 1000 and the watermark lands at day -10 + 2 h; call 2 reads 1000
at day -1; call 3 reads 1000 at -3 h; call 4 returns 0 with the watermark at now - 1 min). A window that has data always
returns `n > 0`, so the Go loop (`n == 0 -> stop`) only stops at the true end. `0` is also returned when another run
holds the advisory lock (benign). Bound: 50 windows x 2 h = 100 h per run, runs every minute, so a multi-day backlog is
rate-limited by the database (below), not by the loop. The purge runs after the loop in the same goroutine, so during a
long catch-up it waits for the loop (a delay, not a loss).

Cost model, measured: time = about 50 us per (session, minute) bucket + about 3 us per row.
* Design load of the lead (482,000 rows / 2 h, 2,000 sessions, about 2 rows per bucket): **12.4 s**.
* 2.0 M rows / 2 h, 1,400 sessions: 14.1 s. A 2-hour window never holds more than sessions x 120 buckets, so 2,000
  sessions cost at most about 12 s in bucket work; the rest is per-row.
* The row rate is NOT 67/s. The sample is written per session and per cut-off period (`public.enforcement_policy.cutoff_seconds`,
  default 5, allowed 1..60) whenever the counters moved: 2,000 active sessions at 5 s = 400 rows/s (2.9 M per 2 h, about 21 s here);
  at the allowed 1 s = 2,000 rows/s.
* **Poison window demonstrated**: 2,000 sessions at a 1 s period, 7.2 M rows in one hour, `rollup(..., '2 hours')` as svc_acctd:
  attempt 1 `canceling statement due to statement timeout` after 30.1 s, attempt 2 the same after 30.1 s; `site_usage_rollup_state`
  0 rows and `accounting_usage_minute` 0 rows afterwards (atomic rollback). A 15-minute window over the same data read
  1.8 M rows in 4.6 s. Without a change the failure repeats forever (the window start is the unmoved watermark), the raw table
  grows without bound and the purge removes nothing.

Fix (no migration): `Enforcer.RollupAccountingUsage` now keeps a per-site window, 2 h at start, divided by 4 after a
statement-timeout error (SQLSTATE 57014), floor 1 minute (the function's minimum), doubled after a call that took under 5 s.
Four cancelled statements (about 2 min) reach the minimum from 2 h; 1 minute of the heaviest allowed load (2,000 rows/s)
is 120 k rows, about 3 s. Lock timeouts and lost connections do not change the window (not a size problem).
Test: `TestIntegration_UsageRollup_APoisonWindowShrinksUntilItFits` (scaled timeout 300 ms, 200 k rows): before the fix
300 of 300 calls cancelled, 0 rows rolled up; after: 4 cancelled, then all 200,000 rolled up and raw = minute = hour exactly.
Unit test `TestNextRollupWindow`. A single `ingested_at` instant with more rows than fit cannot be split; not reachable
(each sample is its own transaction).

## 2. Interruption and retry

The function is one statement in one transaction: upserts into minute and hour and the watermark upsert commit together,
and the advisory lock is transaction-scoped. Measured on a 300 k-row, 300-session window:
* 12 attempts with a random `statement_timeout` of 30-400 ms: 12 cancelled, watermark absent, 0 minute rows after each.
* 5 backends killed mid-call (`pg_terminate_backend`): 0 state rows, 0 minute rows afterwards.
* `lock_timeout=5s` with the state row held by another session: error after 5.8 s, minute rows/samples before = after
  (54282/300000), watermark unchanged.
* 400,000 rows, 20-minute windows, 60 calls alternately killed at a random delay or completed (11 killed, 13 completed
  until drained): per-session comparison of raw vs minute vs hour (bytes up, bytes down, sample count) = **0 mismatching
  sessions**; raw 400000 rows up=200200000 down=1000200000, minute and hour identical. Across the two sites of the earlier
  run: 600 sessions, 660,000 samples, identical. A call whose reply is lost after COMMIT is not re-read (the watermark moved).

## 3. Tick independence

`accountingMaintenance` takes `maintaining.CompareAndSwap(false,true)`, runs roll-up then purge in a goroutine and returns;
a run in flight turns the next call into a no-op. `lastRollup`/`lastPurge` are touched only inside that goroutine, one at a time.
The tick's measuring (`accountingPass`), limits, sweep and submit never wait for it. The roll-up holds one pooled
connection (pgxpool default is at least 4; the role allows 10). The only writer of `accounting_records` is
`ingest_absolute_counters`, a separate autocommit call per session; the roll-up only reads it (no conflicting lock) and
writes other tables; the purge takes row locks only on the rows it deletes.
Measured with `pgbench -c 2` inserting into `iam_v2.accounting_records` (triggers off) while a 13.1 s roll-up of 3.6 M rows ran:
p50 4.5 ms vs 4.6 ms, p99 7.2 ms vs 16.2 ms, max 30 ms vs 198 ms, no insert blocked. With a roll-up blocked on a lock for the
full 5 s (`lock_timeout`): p50 4.4 ms, p99 7.0 ms, max 18.8 ms. Not measured: the real `ingest_absolute_counters` path under this
load, nor the 1 vCPU box.

## 4. Retention safety

`ingested_at` is `now()` = transaction START (`ingest_absolute_counters` writes `now()` explicitly). The roll-up cap is
`now() - 1 minute`. A transaction that stays open more than a minute can commit a row whose `ingested_at` is below the
watermark; the roll-up never reads it, and tier 1 of the purge (`ingested_at < least(now() - raw_retention_days, watermark)`) removes it
later. Demonstrated with two sessions: session X inserted (ingested_at 14:50:20) and held its transaction 75 s; a roll-up
at X+70 s moved the watermark to 14:50:30 (past the row); after X committed (14:51:35) the roll-up returned 0, minute rows
held 100 of 1100 raw bytes_up. A row with `ingested_at` 49 hours old committed below the watermark was deleted by
`purge_accounting_detail` (returned 1), never rolled up. Purge never removes a row the roll-up has not reached: with a 6-day
backlog and only 10 windows rolled, purge deleted exactly the 8,340 rows below the watermark and kept the rest.
Reachability: the production writer is one autocommit statement under `svc_acctd` (`statement_timeout` 30 s, `lock_timeout` 5 s),
so a transaction cannot be older than about 30 s at commit; the 60 s margin holds. It breaks if the role's timeout is raised
to 60 s or more, if another writer holds a transaction open, or if the system clock steps back by more than the margin
(rows then get an `ingested_at` below the watermark). Not fixed: closing it needs a per-row proof of having been read
(a column or a monotonic key) or an `ingested_at` clamp inside the ingest functions, i.e. a migration on the
accounting/enrollment core (CONTROLLED); recommend a PO decision. Not verified: restore and site-migration copies of raw rows.

## 5. Totals and counters

Roll-up writes `accounting_usage_minute`, `accounting_usage_hour`, `site_usage_rollup_state`. Purge deletes from
`accounting_records`, the two usage tables and appends `accounting_purge_runs`. Neither writes `entitlements`, `sessions`,
`accounting_checkpoints`, `data_crossing_progress/state` or the watermarks: `pg_stat_user_tables` write counters of those were
identical before and after a roll-up + purge run. `consumed_data_bytes` and the 0122 incremental crossing
(`p6_advance_data_crossing`, checkpoint by `sample_seq`) do not depend on retained raw rows.
**Finding (not fixed, outside this PR and controlled)**: functions that re-sum raw samples now see only about
`raw_retention_days` (2 by default; 12 months before 0175): `iam_v2.entitlement_usage_bytes` (used by
`checkout.go` for the post-stay quota test and the boundary watermark), `p3_entitlement_data_usage`, and the exact
recount inside `p6_advance_data_crossing`/`p6_data_crossing_full`. Measured: a 6-day single-binding session whose raw samples summed
600,000,000 bytes read 516,600,000 from `p3_entitlement_data_usage` after the purge reached the watermark. A stay longer than
the raw retention can therefore look like it used less than it did at check-out. Needs a PO decision (read `consumed_data_bytes`
or keep raw for the entitlement's life).

## Commands used (all against the local container `og-rollup-review`)

```
. scripts/lib-fullschema-testdb.sh; fullschema_build og-rollup-review 55592 rollupdb   # full schema, then docker update -m 2g --cpus 2
-- load: ogt.gen(tenant, site, n, start, span_s, sessions, seq_base): INSERT INTO iam_v2.accounting_records ... SELECT
--   md5((g % sessions)::text || site)::uuid, seq_base+g, g%1000+1, g%5000+1, start+span*g/n-2s, start+span*g/n FROM generate_series(1,n) g
--   (inside the function: SET LOCAL session_replication_role = replica)
SET ROLE svc_acctd; SET statement_timeout='30s'; SET lock_timeout='5s';
SELECT iam_v2.rollup_accounting_usage(:tenant,:site, interval '2 hours');     -- sites: 482k rows, 2.0M rows, 7.2M rows (poison)
-- reconcile: raw vs minute vs hour per session, FULL JOIN of three GROUP BY session_id subqueries, rows where (up,down,n) differ -> 0
-- kills: psql in background with application_name='og_rollup_victim'; pg_terminate_backend(pid) after 0.3-0.7 s
-- inserter: pgbench -n -f ins.sql -c 2 -T 25 -l   (ins.sql: SET session_replication_role=replica; INSERT one accounting_records row)
-- late commit: BEGIN; INSERT ... (ingested_at default now()); SELECT pg_sleep(75); COMMIT;  while a roll-up runs at +70 s
go test -tags integration -run 'UsageRollup|NextRollupWindow|RollupWindows' ./internal/enforce/   # golang:1.26.10 container, PHASE3_TEST_DSN
```
