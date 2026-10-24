# Phase 5: Background jobs and accounting

**Audited:** master `653363d7`. Code and repository only: nothing was run against PRE-LIVE.

**Covers:**
- acctd's once-a-second loop (`cmd/acctd/main.go:178-206`) and every step in it:
  - accounting;
  - session limits;
  - the expiry sweep, which includes data-quota checkpoints, online-time accrual and suspension;
  - the accounting-detail purge;
  - the shaping plan and its submission to netd.
- The database functions those steps call (`internal/enforce`, the `iam_v2.p6_*` functions).
- Every scd periodic loop:
  - posting;
  - card reconciliation;
  - guest-data retention and log retention;
  - backups;
  - the erasure ledger;
  - self-service;
  - walled garden;
  - PMS health;
  - Central hello, certificate and licence.
- The sign-in-attempt and throttle cleanups.
- The items earlier phases handed on:
  - Phase 1 D1-H2 (the batched accounting design);
  - Phase 4 D4-M2 (acctd's plan build and its blocking call to netd).

**How:**
- **Database:** a throwaway full-schema database (`scripts/lib-fullschema-testdb.sh`), limited to 2 CPUs and 4 GB with the reference settings. It was filled with generated data only:
  - **one year of history:**
    - 366,000 entitlements;
    - 732,000 sessions with their bindings;
    - 400,000 devices;
    - 9.2 million accounting rows;
  - **live, at the design load:** 2,000 sessions on 1,000 entitlements, a third each on a data-quota plan, an online-time plan and a plain plan.
- **Calls:** the real database functions were called in the order acctd calls them. Each tick first added one new sample per live device. WAL was read before and after each step.
- **Caveat:** the workstation's disk is not the appliance's. Phase 11 repeats the key numbers on a reference VM.
- **Compliance:** checked against the [README](README.md#compliance) rules.

## Summary

| Critical | High | Medium | Low | Clean code |
|---|---|---|---|---|
| 0 | 2 | 2 | 2 | 0 |

**Main result.** acctd's expiry sweep has a missing index at its centre: `iam_v2.sessions` cannot be searched by entitlement.

1. **Online-time charging takes 16–19 seconds per pass, against 0.2 s with the index.** Every second, the sweep reads all 732,000 sessions of the year once for each entitlement on an online-time plan. It holds row locks on those entitlements the whole time. Another transaction waited **16.8 s** for one of them. scd gives up after 5 s, so a guest's next device, a new grant or a staff device release fails during the sweep (D5-H1).
2. **The sweep stops altogether as history grows.** acctd's statements are limited to 30 s. At this year's history, about 590 entitlements on online-time plans reach that limit. After that, every sweep fails, and **no data quota or online-time allowance is enforced** (D5-H1).
3. **One slow step holds up every other step.** acctd runs accounting, expiry, limits and the netd submission one after another in one loop. With the index, one pass at the design load takes about **17 s**; without it, about 35 s. Online time in a gap over 60 s is recorded and never charged (D5-H2).

The guest-data retention run keeps pace: 1.1–1.6 s per batch of 500 against its 30 s limit.

## Measured costs

All costs are per tick, at 2,000 live devices and with one year of history. Each range covers several ticks in steady state.

| acctd step | Without the D5-H1 index | With it | WAL per tick |
|---|---|---|---|
| A. Data-quota checkpoints (`p6_advance_data_crossing`) | 182–195 ms (first pass 5.9 s) | 171–195 ms | 285–303 kB |
| B. Expiry candidates (`p6_data_crossing` per entitlement) | 71–81 ms | 76–85 ms | under 2 kB |
| C. Online-time accrual (`p6_tick_online_time`) | **16.4–18.8 s** | **189–209 ms** | 587–602 kB (one tick 8 MB) |
| D. Suspension (`p6_suspend_over_budget`) | 89–131 ms | 101–107 ms | under 1 kB |
| E. Session limits (`p6_end_sessions_over_plan_limits`) | 105–124 ms | 122–124 ms | 73–82 kB |
| F. Shaping plan (`PlanForSite`) | 82–95 ms | 82–95 ms | 0 |
| G. Live-session list for accounting | 50–65 ms | 50–53 ms | 0 |
| H. Detail purge (hourly) | 71–118 ms | 71–82 ms | under 8 kB |

Steps A to D run in one transaction (`EnforceExpiries`, `internal/enforce/enforce.go:251`).

| Other measurement | Result |
|---|---|
| 100 entitlements expiring in one sweep, such as a checkout wave (`p6_expire_entitlement`) | **10.0 s** without the index; **0.31 s** with it |
| A second transaction locking the first entitlement the sweep locked | Waited **16.8 s**: the sweep's whole duration |
| Guest-data retention, `run_data_retention` with scd's batch of 500, 30 s limit, a 30,000-device backlog | 1.08–1.63 s per pass, over 7 passes; no growth |
| Backup of the one-year database (2.9 GB), `pg_dump` and gzip as the backup does | 551 MB, 92 s |
| SHA-256 of that backup | 0.41–0.51 s in Go (1.07–1.34 GiB/s on the workstation); 4.2–4.3 s with `sha256sum` in the 2-CPU container |

## Findings

### D5-H1: Sessions cannot be found by entitlement, and the expiry sweep looks them up that way every second (High, Measured)

**Where:**
- `iam_v2.sessions` has only its primary key and two unique keys that start with `tenant_id`. There is no index on `entitlement_id`, in the baseline or in any later migration.
- Every second, `p6_tick_online_time` loops over the entitlements whose plan counts online time and locks them (`FOR UPDATE`). For each one, it reads that entitlement's sessions (`0000_production_baseline.sql:10357`, `:10403`, `:10407`).
- Expiry looks sessions up the same way:
  - `p6_expire_entitlement`;
  - the fallback `revoke` (`internal/enforce/enforce.go:452`);
  - `p6_suspend_over_budget`.
- Nine database functions filter `sessions` by entitlement. They include these sign-in and staff paths:
  - `authorize_entitlement_device`;
  - `rebind_session_entitlement`;
  - `client_device_action`;
  - `staff_release_device_slot`;
  - `p6_guest_release_device`;
  - `p4_end_access_funded_by`.

**Problem.** Each lookup is a sequential scan of every session ever recorded. With one year of history, that is 732,000 rows and about 80 ms per entitlement (`EXPLAIN ANALYZE`: 17,022 buffers for 2 rows).

**Measured:**
- **Online-time accrual:** 16.4–18.8 s per tick with 333 online-time entitlements; 0.19–0.21 s with `CREATE INDEX ON iam_v2.sessions (entitlement_id)`.
- **Ending 100 entitlements in one sweep:** 10.0 s; 0.31 s with the index.
- **Locks:** a second transaction waited 16.8 s for an entitlement row the sweep had locked.

**Impact.**
- **Guest and staff actions fail during the sweep.** The sweep holds entitlement locks for its whole length. These functions lock the same rows:
  - `authorize_entitlement_device`, used when a guest's next device signs in;
  - the grant kernel (`p4_entitlement_grant_kernel_v2`);
  - `staff_release_device_slot`, `p6_guest_release_device` and `client_device_action`.

  scd and edged run with `lock_timeout=5s`, so those calls fail while a sweep longer than 5 s holds the row.
- **The sweep stops altogether as history grows.** The cost grows with the number of sessions ever recorded, multiplied by the number of online-time entitlements. acctd's role has `statement_timeout=30s`. Each scan costs about 51 ms per online-time entitlement, so the limit is reached:
  - with one year of history, at about 590 such entitlements;
  - with this year's mix, at about 1.7 years of history.

  From then on, every `EnforceExpiries` transaction fails:
  - **no data quota or online-time allowance ends anything**;
  - an entitlement whose window ended is still torn down by the shaping plan, because `PlanForSite` checks `window_ends_at` itself, but it is never terminated.
- **Checkout waves are slow.** A wave of 300 expiries holds the sweep transaction and its locks for about 30 s.
- **Every device sign-in pays the scan too.** `authorize_entitlement_device` reads the entitlement's sessions the same way, at about 80 ms per sign-in with one year of history. That adds to Phase 2's sign-in costs.

**Recommended fix.**
1. Add `CREATE INDEX ON iam_v2.sessions (entitlement_id)`. A full index, because accrual also reads ended sessions whose watermark is behind.
2. Put it in the same migration as Phase 1's B1 indexes. Size: about 20 MB per year of sessions.

**Proof.**
- Step C stays under 250 ms at 2,000 devices with one and with two years of history.
- `EXPLAIN` of the per-entitlement session read shows an index scan.
- A 100-expiry wave stays under 0.5 s.

**Batch:** B1. CONTROLLED (database schema; the guest enforcement core).

### D5-H2: acctd runs every step in one serial loop, so the slowest step sets every deadline (High, Measured parts, estimated total)

**Where.** `cmd/acctd/main.go:182-206` runs these one after another on one goroutine, every tick (1 s by default):
1. accounting (`accountingPass`);
2. session limits;
3. the expiry sweep;
4. the purge;
5. the fallback aggregate sweep;
6. the shaping plan and its submission to netd.

The netd call uses a 10 s client timeout (`cmd/acctd/phase3.go:302`). A `time.Ticker` drops ticks that fall due while a pass is running.

**Problem.** Nothing in a pass starts until the step before it finishes. At the design load, one pass costs:

| Step | Time | Source |
|---|---|---|
| Accounting, one call per device | 6.4–6.9 s, and about 30 s with D1-H1's growth | Phase 1 D1-H2 |
| Expiry sweep, A to D | about 0.5 s, and 17.6 s without D5-H1 | this phase |
| Session limits, plan, live list | about 0.3 s | this phase |
| netd submission | up to the 10 s timeout, since netd's pass takes about 75 s | Phase 4 D4-H2 |

**About 17 s per pass with the index, about 35 s without it, and over 60 s once D1-H1's growth adds in.**

**Impact.**
- **Limits act late.** Data quotas, idle timeouts, maximum session lengths and time allowances end access up to one pass late. They are dated at their true instants, but the guest keeps using the service until the pass reaches them.
- **Online time goes uncharged.** Accrual charges at most `aggregateChargeBoundSeconds` per gap: four ticks with a 60 s floor, so 60 s at a 1 s tick (`phase3.go:72`). A longer gap between passes is recorded as `UNOBSERVED_GAP` and never charged. Once a pass exceeds 60 s, an online-time package lasts longer than it was sold for.
- **New plans arrive late.** netd receives a plan only once per pass, which adds to Phase 4's activation delay.

**Recommended fix.** Split the loop into independent loops, each with its own ticker and deadline:
1. **Submission.** Hand the plan to a submitter that keeps only the newest plan, so a slow netd never blocks accounting or expiry. Together with D4-M2's skip-unchanged, an unchanged plan is not sent at all.
2. **Expiry and limits.** The sweep runs on its own ticker. Ending instants are re-derived from durable state, so running out of step with accounting does not misdate them (`enforce.go:373-376`).
3. **Accounting.** Use Phase 1's batched design (below). Session limits keep judging idleness only after a clean accounting pass (`phase3.go:106`), so the split must pass that fact between the loops.

**Proof.**
- Run with 2,000 generated devices against a netd stub that takes 75 s.
- Accounting and expiry keep their own period: under 2 s each after B1 and D1's batches.
- No `UNOBSERVED_GAP` rows are written.

**Batch:** B2. CONTROLLED (the guest enforcement core).

### D5-M1: The sweep rewrites rows every second whether or not anything changed (Medium, Measured)

**Where and what each step writes per tick:**
- **A** (`p6_advance_data_crossing`, `baseline:9336`) upserts `data_crossing_progress` for every binding of every quota entitlement, even when the binding gained no records.
- **C** upserts `session_online_watermarks` for every session on an online-time plan and updates the entitlement.
- **E** (`p6_end_sessions_over_plan_limits`, `baseline:9584`) takes `FOR UPDATE` on every live session whose plan has a limit, in order to end a few of them. A row lock is a write.

**Measured:** about **0.97 MB of WAL per tick**: A 285–303 kB, C 587–602 kB, E 73–82 kB. That does not include the per-device accounting writes measured in Phase 1.

**Impact.**
- **Write volume:** about 84 GB of WAL a day at a 1 s tick, on the appliance's one disk.
- **Vacuum load:** a stream of dead tuples on `entitlements` and `sessions`, the tables sign-in reads. Phase 1 D1-L3 measured only about 45% of updates as HOT.
- **Checkpoints:** more full-page writes after each checkpoint.

**Recommended fix.**
1. In A, skip the upsert when a binding has no new records (`v_max IS NULL`).
2. In E, select the due sessions without locking, then end each one with an `UPDATE ... WHERE state = 'active'` that re-checks it.
3. The tick-length decision from Phase 1 (5 s recommended) cuts the rest to one fifth.

**Proof.** WAL per tick, measured as here, is under 200 kB at 2,000 devices, and the results match the current functions on the same data.

**Batch:** B3. CONTROLLED (database functions in the enforcement core).

### D5-M2: Every 15 minutes, scd re-reads and re-hashes every stored backup in full (Medium, Measured parts)

**Where:** `runScheduledBackupIfDue` calls `storedBackupProblems(0)` on every 15-minute pass (`cmd/scd/backups.go:575`). The `0` bypasses the 20-minute cache, and `verifyStoredBackups` hashes every file of every backup (`:450-475`). The default keeps 7 regular and 3 protective backups (`:373`).

**Measured.** The one-year generated database dumps to 551 MB. Its SHA-256 costs 0.4–0.5 s of CPU in Go; the cost that matters is the disk read.

**Impact.**
- **Disk reads:** with 10 backups, about 5.5 GB every 15 minutes, which is about 530 GB a day.
- **Memory:** on a 4 GB appliance this data passes through the page cache that Postgres's settings count on (`effective_cache_size` 3.75 GB, Phase 1 D1-M2), four times an hour. After each check, Postgres reads its working set back from disk.
- **Growth:** the cost grows with database size, the same as the backups.

**Recommended fix.**
1. Every 15 minutes, check only that each file exists and that its size and modification time are unchanged.
2. Re-hash one backup per pass in rotation, so every backup is fully checked at least once a day.
3. Always do a full hash right after writing a backup and right before a restore.
4. Read with `posix_fadvise(DONTNEED)` so a check does not evict the database's cache.

**Proof.** In a run with ten 551 MB backups, block-device reads over 15 minutes fall from about 5.5 GB to one backup's worth. Postgres's buffer hit ratio does not fall after a check.

**Batch:** B4. FAST, but see Compliance.

### D5-L1: With no guests online, acctd still runs about a dozen statements a second (Low, Confirmed)

**Where.** Every tick, even when idle:
- `EnforceExpiries` opens a write transaction, probes the catalog (`resolveDataCrossing`, `enforce.go:82`) and runs A to D;
- `EnforceSessionLimits` and `PurgeAccountingDetail` each probe `to_regprocedure` before their call (`plan_limits.go:32`, `:63`);
- `PlanForSite` and `managedBridges` run;
- the plan is sent to netd.

**Impact.**
- About a million statements a day on an empty appliance.
- At the design load, the fixed part of B to F costs about 0.5 s per tick.

**Recommended fix.**
- Cache the catalog probes for a minute. The code's own concern is a migration applied under a running process, and that is met within the minute.
- Skip the sweep's write transaction when no entitlement is live.

**Batch:** B5. FAST, but the probes sit in the enforcement core, so test with B2.

### D5-L2: Daily log cutting copies each whole log, then empties it in place (Low, Confirmed)

**Where.** `cmd/scd/log_retention.go:5`. Once a day, each file in `/var/log/stayconnect`, `/var/log/caddy` and `/var/log/kea` is copied to `<name>.<YYYYMMDD>` and then truncated.

**Impact.**
- **Cost:** each day's logs are read and written once more. The cost grows with traffic. Caddy's access log is the largest, because of probe traffic (Phase 3).
- **Lost lines:** lines written between the copy and the truncate are lost from both files. ISO 27001 A.8.15 expects logs to be complete.

**Recommended fix.**
- **Writers that can reopen** (Caddy's own roller, Kea's file rotation): rename the file and signal the writer to reopen.
- **Writers that cannot:** keep copy-and-truncate.

**Batch:** B5. FAST.

## Checked, no finding

| What | Result |
|---|---|
| Guest-data retention (`run_data_retention`, hourly, up to 20 passes of 500) | 1.1–1.6 s per pass against the 30 s limit. That gives a capacity of about 10,000 rows per category an hour, against about 2,000 new devices a day. The stays part was fixed by migration 0149 on PRE-LIVE's real data (160 s to 7 s); it was not regenerated here. |
| Accounting-detail purge | 71–118 ms per batch, hourly. Its cap and backlog are Phase 1 D1-H3 and D1-L7. |
| Posting worker (2 s) | The lane query uses the partial index `outbox_backlog_age`. One posting per PMS interface per 2 s, up to 1,800 an hour, which is enough at the target load. Phase 6 measures the FIAS round trip. |
| Card reconciliation (20 s) | At most 25 pending checkouts per pass, bounded by a 2-minute context. Reversal sweep every 6 h, 200 at a time. |
| Throttle cleanup (5 min) and sign-in-attempt purge (hourly) | Both delete by an indexed time column. |
| Self-service send (15 s) and sweep (30 s); walled garden (1 min); PMS health flush (30 s); PMS reload (10 min) | Small fixed queries. |
| Central hello (5 min), certificate (5 min and 6 h), licence re-evaluation (1 min, local) and fetch, offline reconcile | Calls to Central and a local licence reload; no database scans. Phase 9 sizes the calls from Central's side. |
| Erasure ledger (hourly) | One reconciliation with a 5-minute bound. Its size follows the number of erasures, not traffic. |

## Items from earlier phases

| Item | Outcome |
|---|---|
| **Phase 1 D1-H2: a batched accounting design** | **Design:**<ul><li>One database call per pass, or per chunk of about 200 devices, taking arrays: session, device, bridge, class, epoch, absolute up, absolute down.</li><li>The function runs the same per-observation logic for each element: replay and reset classification, the checkpoint, the record, the totals.</li><li>It opens the controlled-writer scope once per call instead of once per device.</li><li>It returns one classification per element, so acctd reports exactly what it reports today.</li><li>One commit per chunk replaces one per device.</li></ul>**Expected cost:** Phase 1 measured 4.8 s for 2,000 devices in one transaction as the lower bound of the per-row form. A set-based form (`unnest` into one insert and one update per table) is expected to be far below it, but this is not yet measured. The proof is to measure it on this phase's data against the 6.4–6.9 s baseline.<br>**Which comes first:** the tick length (Phase 1's decision) sets whether this is needed before Phase 11. It belongs to B2 with D5-H2. |
| **Phase 4 D4-M2: acctd's plan build and its blocking call** | **The build** is cheap: `PlanForSite` takes 82–95 ms, and Phase 1 D1-M1's index removes its sequential scan.<br>**The cost** is the blocking call. acctd waits up to 10 s for netd inside its one loop. That is D5-H2, and B2's newest-plan submitter fixes it. |

## Handed on

| To | What |
|---|---|
| Phase 6 | pmsd's stay engine (checkout conversion, `stayengine.NewProcessorWithCheckout`) and the posting hand-off relay. Posting throughput per lane against the measured FIAS round trip. |
| Phase 7 | edged's own loops: the 10 s health loop and the guest-data reads loop. |
| Phase 10 | Disk and memory budget: about 84 GB of sweep WAL a day at 1 s (D5-M1); backups of 551 MB per year of data, taking 92 s to make (D5-M2); page-cache pressure from backup checks. |
| Phase 11 | Repeat D5-H1 and D5-M1 on a reference VM. Run the full acctd loop at 2,000 devices after B1 and B2, and record the period of each loop. |

## Batches

| Batch | Findings | Notes |
|---|---|---|
| **B1** | D5-H1 | One index, added to Phase 1's B1 migration. **Do first.** It turns a 17 s locked sweep into 0.2 s and removes the timeout cliff. CONTROLLED. |
| **B2** | D5-H2, Phase 1 D1-H2 (batched accounting), Phase 4 D4-M2 (acctd's side) | Independent loops, a newest-plan submitter, batched ingest. CONTROLLED. |
| **B3** | D5-M1 | The write-reduction changes to A and E. CONTROLLED. |
| **B4** | D5-M2 | The rotating backup check. FAST. |
| **B5** | D5-L1, D5-L2 | Cached probes, no idle write transaction, rename-and-reopen logs. FAST; test the probe part with B2. |

## Compliance

| Finding | Rules | What the fix must respect |
|---|---|---|
| D5-H1 | GDPR Art. 32 | An index on an identifier holds nothing new. Today, the failure mode stops limits from being enforced and fails sign-ins; the fix restores enforcement without changing what is enforced. |
| D5-H2 | Consumer and billing accuracy; GDPR Art. 32(1)(b) | Split loops still date every ending at its true instant. Idleness is still judged only after a clean accounting pass, so a broken meter never disconnects a guest. Uncharged gaps stay recorded. The fix makes them rare; it does not hide them. |
| D5-M1 | ePrivacy Art. 6; GDPR Art. 5(1)(c) | Writing less changes no stored traffic data. The functions keep their results exactly. |
| D5-M2 | ISO/IEC 27001 A.8.13; GDPR Art. 32(1)(c), (d) | Backups stay verified: every one is fully re-hashed at least daily, right after it is written, and right before any restore. The integrity alarm keeps its wording and destination. |
| D5-L2 | ISO/IEC 27001 A.8.15; GDPR Art. 5(1)(e) | Logs become more complete. The hotel's `log_days` and the daily cut are unchanged, and nothing is kept longer. |
| Retention run | GDPR Art. 5(1)(e), Art. 17 | Measured to keep pace. No change recommended. |
| All | GDPR Art. 25; README rule 7 | All test data was generated. No guest data was read or reproduced. |

## Tests run

| Test | Result |
|---|---|
| Full-schema database, 2 CPUs and 4 GB, reference settings; one year of generated history and 2,000 live devices | Built in about 3 minutes |
| acctd's steps A to H, four ticks without the index and three with it; WAL per step | Measured costs table |
| `EXPLAIN (ANALYZE, BUFFERS)` of the per-entitlement session read | Sequential scan of 732,000 rows, about 80 ms |
| Lock wait on an entitlement the sweep had locked | 16.8 s |
| 100 expiries in one transaction, without and with the index (rolled back) | 10.0 s and 0.31 s |
| `run_data_retention`, batch 500, 30 s limit, 7 passes over a 30,000-device backlog | 1.08–1.63 s each |
| `pg_dump` and gzip of the test database; SHA-256 with Go and with `sha256sum` | 551 MB in 92 s; 0.41–0.51 s and 4.2–4.3 s |
| Code trace of acctd's loop, `internal/enforce`, the `p6_*` functions and every scd loop | D5-H2, D5-M1, D5-L1, D5-L2, and the Checked list |

**Test-only changes, made inside the throwaway database and never in the repository:**
- The foreign keys on `entitlements` were dropped so that generated plan identifiers could be expired.
- `site_data_retention` was set to 335 days for client activity, to create a backlog.

**Cleanup.**
- **Docker:** the test container was removed with its volume (`docker rm -f -v`).
- **Scratch:** the scripts and the Go hash benchmark were deleted.
- **Image:** the TimescaleDB image was already present and was kept.
