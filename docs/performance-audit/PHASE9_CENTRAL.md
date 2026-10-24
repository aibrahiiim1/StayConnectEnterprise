# Phase 9: Central (ctrlapi)

**Audited:** master `653363d7`, fetched at the start of the phase. Code and repository only: nothing was run against Central or PRE-LIVE.

**Covers:**
- ctrlapi's appliance API on both listeners (`control-plane/internal/api`, `internal/auth/appliance.go`): hello, licence, certificate, assignment, assignment registry, portal TLS.
- The console's fleet routes (`/cloud/v1/appliances`, `/overview`, `/appliances/{id}`, `/licenses`).
- ctrlapi's housekeeping loop (`cmd/ctrlapi/main.go:177-216`).
- Redis rate limiting and the replay cache.
- The items earlier phases handed on:
  - Phase 1: Central's pool of 10;
  - Phase 5: the appliance's Central call schedule, sized here from Central's side;
  - Phase 8: the unpaged appliance list (D8-M2).

**How:**
- **Schedule:** a read-only agent mapped every appliance call to Central in `data-plane`, with its interval, conditions and retry. I checked its key lines.
- **Database:** a throwaway TimescaleDB container with the same image as Central (`timescale/timescaledb:2.16.1-pg16`), limited to 2 CPUs and 2 GB as `deploy/compose/central-infra.yml` limits Central's.
  - The image tuned itself on first start, as Central's does: `shared_buffers` 512 MB, `work_mem` 1.3 MB, `max_connections` 25.
  - All 56 migrations were applied and verified with `deploy/scripts/central-migrate.sh`.
- **Generated data only:**
  - 40 customers;
  - 1,000 appliances (25 waiting for activation), with `.invalid` host names and TEST-NET-1 addresses;
  - 12 licence versions per activated appliance (11,700 licences);
  - 5 certificates each (4,875);
  - 6,000 security alerts;
  - 1,000,000 audit rows over one year.
  - **Fetch log:** 30 days of 30-second polls for 200 appliances, 17.3 million rows. 1,000 appliances would be 86 million, too large for the workstation, so those figures are scaled from 200.
- **Queries:** the console queries were copied from the code and run with `EXPLAIN (ANALYZE)`, three times each.
- **Appliance calls:** the per-call SQL of the signed-JWT path and the assignment fetch was replayed with `pgbench` at the 1,000-appliance call rate.
- **Caveat:** a workstation is not Central's host. Phase 11 repeats the key numbers on reference hardware.
- **Compliance:** checked against the [README](README.md#compliance) rules.

## Summary

| Critical | High | Medium | Low | Clean code |
|---|---|---|---|---|
| 0 | 0 | 2 | 3 | 0 |

**Main result.** The appliances' own calls are cheap for Central. At the 1,000-appliance stress rate (about 77 calls a second), each call's database work averaged 3.4–6.9 ms with no errors. The cost is in two places that grow with the fleet:

- **The hourly fetch-log prune reads the whole table (D9-M1).** Every 30-second assignment poll writes a row that nothing reads, kept for 30 days. With 200 appliances the hourly `DELETE` took 51–77 s and read 1.2 GB. With an index on the timestamp it took 23–68 ms.
- **The fleet list and Overview scan every licence once per appliance (D9-M2).** At 1,000 appliances they take 1.2–1.9 s, and the appliance list page sends both every 30 s. Writing the licence match so it uses an existing index takes it to 0.07–0.16 s, with no schema change.

## Fleet call rate

Steady state for one activated appliance (`data-plane/cmd/scd`, `internal/licstate`, `internal/appliancecert`):

| Call | Interval | Signed JWT | Central's work per call |
|---|---|---|---|
| `GET /v1/appliance/assignment` | 30 s | no (mTLS only) | certificate match, assignment lookup, **one fetch-log insert** |
| `GET /v1/appliance/assignment-registry` | 30 s, before each assignment poll | yes | appliance lookup, **`UPDATE appliances SET last_seen_at`**, certificate binding |
| `GET /v1/appliance/hello` | 5 min | yes | as above |
| `GET /v1/appliance/certificate` (reconcile) | 5 min | yes | as above, plus the certificate read |
| `POST /v1/appliance/portal-tls/sync` | 5 min (30 s while an order is pending) | yes | as above, plus the portal TLS state |
| `GET /v1/appliance/license` | 6 h (1 min while no usable licence) | yes | as above, plus the licence |

| Fleet | Signed calls | Assignment fetches | Total |
|---|---|---|---|
| 200 (design) | 520 a minute (8.7/s) | 400 a minute (6.7/s) | about 15 a second |
| 1,000 (stress) | 2,600 a minute (43/s) | 2,000 a minute (33/s) | about 77 a second |

**Notes on the schedule:**
- **Timing:** only the registration retry has random jitter. Every other loop runs on a fixed timer, and almost all of them call Central immediately when scd starts (D9-L2).
- **When Central is down:**
  - backoff: registration and certificate bootstrap back off exponentially to 5 minutes, and the retirement acknowledgement steps up to 15 minutes;
  - fixed interval: everything else keeps its normal interval.

## Measured

### Appliance calls (1,000-appliance rate, 60 s)

| Path | Calls/s | Mean | Per statement |
|---|---|---|---|
| Signed JWT path | 43.8 | 6.9 ms | lookup 0.66 ms, `UPDATE last_seen_at` 3.1 ms, certificate binding 0.73 ms |
| Assignment fetch | 34.4 | 6.1 ms | certificate match 0.74 ms, assignment 0.36 ms, fetch-log insert 3.2 ms |
| Both again, while the prune ran | 42.2 and 32.0 | 3.8 ms and 3.4 ms | unaffected: the prune blocks neither reads nor inserts |

### Console queries

| Query (route) | 200 appliances | 1,000 appliances | Fixed form (1,000) |
|---|---|---|---|
| Fleet list (`GET /appliances`, `GET /overview`) | 97–173 ms | **1.21–1.95 s** (0.98–1.30 s with JIT off; JIT adds 0.2–0.8 s) | 0.07–0.16 s |
| Appliance detail row (`loadOne`) | | 1.8–2.1 ms | |
| Detail events, appliance with no recent events | | **127–303 ms** at 1 million audit rows | |
| Detail events, appliance with 1,000 events | | 7–8 ms | |
| Licence list (`GET /licenses`) | | 3.6–22 ms | |

### Assignment fetch log (200 appliances, 30 days)

| Measure | Result |
|---|---|
| Rows | 17,304,000 |
| Size | 1.8 GB table, 1.6 GB index, 3.4 GB in all |
| Hourly prune as written | **51.5–76.6 s**, sequential scan, 153,510 pages read (1.2 GB) |
| With an index on `at` | 22.6–68.0 ms (the index is 113 MB, built in 18 s) |
| At 1,000 appliances (scaled from 200) | 86 million rows, about 17 GB; the prune about 4–6 minutes every hour |

## Findings

### D9-M1: The hourly fetch-log prune reads the whole 30-day log (Medium, Measured)

**Where.**
- **Write:** every assignment poll, every 30 s per appliance, inserts a row (`logFetch`, `internal/api/terminal_delivery.go:390-393`).
- **Prune:** rows are kept 30 days and deleted hourly by `DELETE … WHERE at < now() - 30 days` (`appliance_ops.go:773-784`).
- **Index:** the only index is `(appliance_id, at DESC)` (`migrations/0033`), which the prune cannot use.
- **Reads:** nothing in ctrlapi or the console reads this table.
- **Loop:** the prune runs inside the 60 s housekeeping loop, on the root context with no deadline (`cmd/ctrlapi/main.go:177-216`).

**Problem.**
- **The prune reads everything:** every hour it reads the whole table to find one hour of rows.
- **The volume is mostly routine:** 98% of rows are `not-modified` polls.

**Impact.**
- **Database time:** with 200 appliances, 51–77 s every hour, reading 1.2 GB through a 512 MB buffer cache. With 1,000 appliances, about 4–6 minutes every hour.
- **Delayed housekeeping:** while the prune runs, the same loop's retirement timeout and replacement-window checks wait.
- **Disk and backups:** 3.4 GB at 200 appliances and about 17 GB at 1,000, all in Central's database and its backups (`central-export.sh`).
- **Not affected:** the appliances' calls; measured at 3–4 ms during the prune.

**Recommended fix.**
1. **Log less.** Record only `served` and `denied:*` outcomes, and count the `not-modified` polls per appliance per hour instead. The table is a diagnostic, and this keeps what an investigation needs. This is a DECISION: it changes what Central records.
2. **Make the prune cheap,** either way:
   - add an index on `at`;
   - make the table a 1-day TimescaleDB hypertable and drop whole chunks with `drop_chunks`, as Central already does for `audit_log`.

   Either is a migration, so CONTROLLED.
3. **Give the housekeeping statements a deadline** (for example 60 s), so one slow pass cannot hold up the loop. This is FAST.

**Proof.** At 200 appliances with 30 days of polls, the prune takes under 100 ms. At 1,000 appliances the table stays under 1 GB with step 1.

**Compliance.** The log holds an appliance id, a certificate fingerprint and an outcome: no personal data. Logging fewer rows is the minimal choice (GDPR Art. 5(1)(c)). Denials stay logged in full, because they are security events (ISO/IEC 27001 A.8.15).

**Batch:** B1. CONTROLLED (migration); step 1 is a DECISION, step 3 is FAST.

### D9-M2: The fleet list and Overview scan every licence once per appliance (Medium, Measured)

**Where.** `queryAppliances` (`internal/api/fleet.go:78-118`) runs on every call to:
- `GET /cloud/v1/appliances`;
- `GET /cloud/v1/overview` (`fleet.go:524`);
- the customer detail page.

Its licence join is `WHERE a.id = ANY(x.appliance_ids)` (`fleet.go:102`).

**Problem.**
- **No usable index:** the licence indexes are on `appliance_ids[1]`, and partial on `cardinality(appliance_ids) > 0` (`licenses_appliance_version_unique`, `licenses_one_current_per_appliance`). `= ANY` can use neither.
- **The cost grows with the fleet times the licence history:** each appliance scans every licence ever issued, superseded ones included.
- **JIT:** at 1,000 appliances the planner also turns on JIT, which adds 0.2–0.8 s of compile time.
- **Filters in Go:** the handler loads the whole fleet, then filters in Go (`filterAppliances`). Every filter or search costs the full query.

**Impact.**
- **Query time:** 97–173 ms at 200 appliances; 1.2–1.9 s at 1,000.
- **Polling:** the cloud-admin appliance list sends this query twice every 30 s per open tab, for the list and for the Overview counts (D8-M2).
- **At 1,000 appliances:** each open list tab costs 2.4–3.8 s of database time every 30 s, which is 8–13% of one CPU. It also holds 2 of ctrlapi's 10 connections for over a second each time.

**Recommended fix.**
1. **Use the existing index.** Match the licence on `x.appliance_ids[1] = a.id AND cardinality(x.appliance_ids) > 0`. Every licence is issued for exactly one appliance (`internal/licensing/licensing.go:214`), so the result is the same. Measured at 0.07–0.16 s at 1,000 appliances. This is a query change, FAST.
   - The same pattern appears in the per-appliance licence lookups (`licensing.go:202-291`, `licenses.go:243, 274`, `appliance_lifecycle_policy.go:35`). They run rarely, but should be changed with it.
2. **Page and filter in SQL** (50–100 rows, with the filters in the `WHERE`), and return the counts with the page. That is D8-M2's server half. The cloud-admin side changes with it.
3. **Optional:** let the Overview count states with one aggregate rather than building every row.

**Proof.** At 1,000 appliances with 12 licence versions each, the list and the Overview each answer under 200 ms, and a page of 100 rows is under 60 kB.

**Compliance.** Paging must keep the role scope (`readScope`, `scope.Filter`). A customer-scoped operator still sees only that customer's appliances (GDPR Art. 25, 32).

**Batch:** B2. FAST.

### D9-L1: The appliance detail page reads the whole audit log for a quiet appliance (Low, Measured)

**Where.**
- **The query:** `applianceEvents` (`fleet.go:295-315`) filters `audit_log` by `(target_type = 'appliance' AND target_id = $1) OR payload->>'appliance_id' = $1`, newest first, `LIMIT 50`.
- **Indexes:** `audit_log` has indexes only on `ts` and `(tenant_id, ts)`.
- **The page:** it polls every 30 s, or every 5 s while the appliance is activating (`cloud-admin/app/(app)/appliances/[id]/page.tsx:91`).

**Problem.** For an appliance with fewer than 50 matching events, the query walks the whole audit log backwards before it stops. That is most appliances, most of the time.

**Impact.**
- **Per call:** 127–303 ms at one million audit rows. The log is kept 24 months by default and up to 120 (`retention.go:32`), so the cost grows with time.
- **While activating:** a 5 s poll uses about 3–6% of one CPU while the page is open.
- **Busy appliance:** one with 50 recent events answers in 7–8 ms.

**Recommended fix.** Make the two conditions indexable and combine them with `UNION ALL`, keeping `ORDER BY ts DESC LIMIT 50`:
- an index on `(target_type, target_id, ts DESC)`;
- an expression index on `((payload->>'appliance_id'), ts DESC)`.

On a hypertable each chunk gets its own copy. This is CONTROLLED (migration).

**Proof.** Under 20 ms for an appliance with no events, at 1 and 5 million audit rows.

**Compliance.** The same rows, to the same operators. The audit log itself is unchanged (ISO/IEC 27001 A.8.15).

**Batch:** B1. CONTROLLED.

### D9-L2: Appliance calls have no jitter, and all start at boot (Low, Confirmed)

**Where.**
- **Fixed timers, immediate first call:**
  - hello: `cmd/scd/main.go:1069-1070`;
  - assignment and registry: `assignment.go:142-144`;
  - portal TLS: `portal_tls.go:287-296`;
  - licence: `internal/licstate/licstate.go:515`;
  - certificate reconcile: `main.go:1142-1145`.
- **Jittered:** only registration (`registration.go:49-51`).

**Problem.**
- **No spreading:** appliances that start together keep calling together.
- **Upgrades restart scd:** a fleet upgrade, or a scheduled restart at the same hour, starts many appliances at once. scd also re-execs after registration and after an assignment change.

**Impact.**
- **Design load:** none. 1,000 appliances starting together send about 6,000 calls in a few seconds. At the measured 3–7 ms each, across 10 connections, that drains in under 5 s.
- **Rate limits:** the limits are per client address (240 a minute on the mTLS group), so appliances sharing one public address share a budget.

Phase 11 measures the burst.

**Recommended fix.**
- Add a random first delay of 0–30 s, and ±10% jitter, to each Central loop in scd.
- This touches licensing and assignment timing on the appliance only, with no protocol change. FAST.

**Batch:** B3. FAST.

### D9-L3: Every signed call writes `appliances.last_seen_at` (Low, Measured)

**Where.** `RequireAppliance` runs `UPDATE appliances SET last_seen_at = now(), version = …` on every signed call (`internal/auth/appliance.go:119-122`). At 1,000 appliances that is 2,600 updates a minute to a 1,000-row table.

**Impact.**
- **Per call:** 3.1 ms on average, half the cost of each signed call.
- **Churn:** a steady stream of dead row versions and WAL for a value the console reads to the minute (`DeriveState`).

**Recommended fix.** Write only when the stored value is older than 60 s, or the version changed:

```sql
… WHERE id = $1 AND (last_seen_at < now() - interval '60 seconds' OR version IS DISTINCT FROM $2)
```

The connection states stay correct to the minute. FAST.

**Batch:** B2. FAST.

## Measured clean

| Area | Result |
|---|---|
| Appliance call path at the 1,000-appliance rate | 3.4–6.9 ms mean per call, no errors, also during the prune. |
| Pool and connections | ctrlapi's pool of 10 fits the tuned `max_connections` of 25. Phase 1's question is answered for Central. |
| Replay cache (`applianceauth/jwt.go:173-193`) | It never refuses a fresh token when full. It sweeps expired entries and grows. At 1,000 appliances it holds about 5,200 tokens per 2 minutes, under its 8,192 sweep mark. |
| Redis rate limits (`ratelimit.go`) | One counter per call. On a Redis error it fails open, so a Redis outage does not cut appliances off. |
| Licence list, detail row | 3.6–22 ms and 1.8–2.1 ms. |
| Audit retention | `central_retention_apply()` deletes by `ts`, which the timestamp index and TimescaleDB chunk exclusion serve (`migrations/0056`). |

## Items from earlier phases

| Item | Outcome |
|---|---|
| Phase 1: Central's pool of 10 | Fits `max_connections` 25. Under the measured load the per-call path did not queue. D9-M2's list query is what holds connections long. |
| Phase 5: appliance calls to Central | Sized above: about 4.6 calls a minute per appliance; 15 a second at 200 and 77 at 1,000. D9-L2 for the missing jitter. |
| Phase 8: unpaged `GET /cloud/v1/appliances` (D8-M2) | Confirmed. The handler loads and filters the whole fleet in Go. Server paging is D9-M2 step 2. The query itself is D9-M2 step 1. |

## Handed on

| To | What |
|---|---|
| Phase 10 | Central's disk: the fetch log at 3.4 GB (200 appliances) to about 17 GB (1,000), and what it adds to Central's backups (D9-M1). |
| Phase 11 | A 1,000-appliance boot burst against a real ctrlapi (D9-L2). The fleet list with several operators at 1,000 appliances, before and after D9-M2. All key numbers on Central's reference hardware. |

## Batches

| Batch | Findings | Notes |
|---|---|---|
| **B1** | D9-M1 (index or hypertable), D9-L1 | One Central migration. CONTROLLED. D9-M1 step 1 needs the Product Owner's decision on what the fetch log records. |
| **B2** | D9-M2, D9-L3, D9-M1 step 3 | ctrlapi query and handler changes, with D8-M2's cloud-admin half. **Do first.** FAST. |
| **B3** | D9-L2 | Jitter in scd's Central loops. FAST. |

## Compliance

| Finding | Rules | What the fix must respect |
|---|---|---|
| D9-M1 | GDPR Art. 5(1)(c); ISO/IEC 27001 A.8.15 | No personal data is in the log. Recording fewer routine rows is the minimal choice; denials stay logged. |
| D9-M2 | GDPR Art. 25, 32 | Paging and SQL filters keep `readScope`'s customer scope. |
| D9-L1 | ISO/IEC 27001 A.8.15 | The indexes change how the audit log is read, not what it holds or how long. |
| D9-L2, D9-L3 | CLAUDE.md §0E | No new kind of message from the appliance to Central. Timing and write frequency change only. |
| Test data | README rule 7 | Generated data only: test customers, `.invalid` hosts, TEST-NET-1 addresses, placeholder keys and certificates. |

## Outside this audit

- **Server timeouts:** ctrlapi's console listener sets `ReadHeaderTimeout` only (`cmd/ctrlapi/main.go:294-298`), with no write or idle timeout. That is a robustness matter for the backend audit, not a performance finding.

## Tests run

| Test | Result |
|---|---|
| TimescaleDB 2.16.1-pg16 container, 2 CPUs and 2 GB, self-tuned; 56 migrations applied and verified with `central-migrate.sh` | Built |
| Generated fleet: 1,000 appliances, 11,700 licences, 4,875 certificates, 1 million audit rows; 17.3 million fetch-log rows for 200 appliances | Loaded |
| `EXPLAIN (ANALYZE)` of the fleet list (1,000 and 200 appliances), detail row, detail events, licence list; three runs each | Console queries table |
| Fleet list with `appliance_ids[1]`, with `@>` and a GIN index, with JIT on and off | D9-M2 |
| Hourly prune, as written and with an index on `at`, three runs each, rolled back | D9-M1 |
| `pgbench` of the signed-JWT path and the assignment fetch at 43 and 33 calls/s for 60 s, then again during the prune | Appliance calls table |
| Read-only map of every appliance call to Central in `data-plane` | Fleet call rate |

**Test-only changes, made inside the throwaway database and never in the repository:**
- The 200-appliance figures were taken by deleting the other 800 in a transaction that was rolled back.
- The prune timings ran in transactions that were rolled back.
- The GIN index and the `at` index were created for one measurement each and dropped.

**Cleanup.**
- **Docker:** the container was removed with its volume (`docker rm -f -v`).
- **Scratch:** the generator and query scripts were deleted.
- **Repository:** `git status` is clean.
- **Not mine:** three volumes and their containers appeared during the phase from the `sop` compose project (Postgres 17, SeaweedFS). This repository does not use them, and they were left alone.
- **Image:** the TimescaleDB image was already present and was kept.
