# OneGate appliance sizing guidance: DRAFT from lab measurements (2026-10-31)

**Status: draft, for the Product Owner's review. Not a specification, not a benchmark.** It answers one question: what can be said about an appliance of 4 to 8 vCPU, about 16 GB of memory and 100 to 150 GB of storage from what was measured, and what cannot.

**The only hardware that was measured is the lab appliance: a container capped at 2 vCPU and 4 GB on a Docker Desktop VM, on the same host as the load generator and the "internet" sink.** Nothing was measured on 4 to 8 vCPU, on 16 GB or on a real disk. Every statement about larger hardware below is marked **EXTRAPOLATION** and is arithmetic on a measured figure, not an observation. Where the arithmetic would need a property that was not measured (how netd scales across cores, PostgreSQL behaviour with a larger buffer pool, fsync latency of the target disk) it is left unwritten.

Measurements: [`evidence/final-mission-2026-10-30/capacity/`](evidence/final-mission-2026-10-30/capacity/), run of 2026-10-31 05:42 to 06:44 UTC, release `onegate-appliance-5926f7dead47` with netd from the local branch `lab-fixes-1`; method in [`CAPACITY_AND_OFFLINE_ACCEPTANCE.md`](CAPACITY_AND_OFFLINE_ACCEPTANCE.md). A first run of the same hour (04:31 UTC, `capacity-run1`) gave the same offered, enforced and latency figures (18,001 of 18,001; p50 2.97 s, p99 5.22 s); its CPU figures were wrong (instrument fault, see the commit message) and are not used.

## 1. The workload that was measured

1,700 long-lived sessions on two `/20` client networks (10 percent of them moving 256 KB every 5 s), **plus 300 sign-ins a minute for 60 minutes of 60-second vouchers** from 400 simulated clients (18,001 sessions created and ended), licence 2,500. This is a stress shape, not a hotel's day: a hotel with 2,000 devices signs a few thousand devices in per day, not 18,000 in an hour.

| Measured in the hour | Value |
|---|---|
| Offered sign-ins (generator's own fixed schedule) | 18,001, exactly 300 in every one of the 60 minutes |
| Enforced (session `active` and its kernel authorization in place; activation timestamps taken inside the appliance's database) | 18,001; per full minute 289 to 309 (median 301); per ten minutes 2,989 / 3,000 / 2,996 / 3,004 / 2,994 / 3,004 |
| Portal request to success | p50 3.09 s, p95 4.53 s, p99 5.13 s, max 7.31 s, 18,001 inside 8 s |
| Authorization set | never below 1,736 (1,700 long sessions plus the churn), 0 devices with a second live session, licence count at most 2,060 of 2,500 |
| CPU of the appliance's 2 vCPUs (5 s windows) | mean 80.6 percent, p50 79.1, p95 95.4, p99 99.6, max 102.4; 37 of 631 windows at or above 95 percent, 7 at or above 100 percent |
| Saturation (kernel pressure, `some avg10`) | CPU 41 percent mean (p95 45.7, max 53.4); I/O 3.8 percent mean (max 6.3); memory 0 |
| CPU by service (mean, percent of one CPU, twelve one-minute windows) | netd 66.8, PostgreSQL 58.1, Docker 4.7, scd 3.9, portald 3.8, acctd 3.8, Kea 1.1, containerd 1.0; total 152 of 200 |
| Memory | container `memory.current` up to 3.1 GB (mostly page cache), 0 OOM and 0 high/max events, memory pressure 0; resident scd 33 MB, edged 27, portald 29 to 40, acctd 19 to 35, netd 43 to 108 |
| PostgreSQL | 21 to 25 connections (scd at most 8, acctd 6, netd 7, edged 2); lock waiters at most 6 (45 of 632 samples had any), ungranted locks at most 6, idle in transaction at most 2; 0 deadlocks, 0 lock timeouts, 31 `still waiting for` lines of `log_lock_waits`; slowest statement classes: `ingest_absolute_counters` (322,010 calls, mean 2.6 ms), `p6_suspend_over_budget` (623 calls, mean 465 ms, max 1.4 s), `end_session_enforcement` (10,064 calls, mean 21 ms) |
| Forwarding | 122,400 of 122,400 downloads by the moving 10 percent succeeded (32.1 GB), p50 100 ms, p99 155 ms |
| Accounting integrity | raw, per-minute, per-hour, per-session and per-entitlement byte totals all equal (273,952,681,817 bytes cumulative in the lab database), 0 devices with a second live session |
| Healthy steady state without churn (1,735 sessions, 10 percent moving, ten minutes) | netd 16, PostgreSQL 27, acctd 3: **50 percent of one CPU in all** |

**The 2 vCPU / 4 GB reference met the target at the edge:** a mean of 81 percent with a 99th percentile of 99.6 percent means any additional load, a slower disk or a noisy neighbour would show first as portal latency. That is a measurement of the reference, not of the larger machine.

## 2. CPU

* Measured: 1,735 sessions with 10 percent movers cost **0.50 CPU** (netd 0.16, PostgreSQL 0.27, acctd 0.03 of one core); adding 300 sign-ins a minute raised the total to **1.52 CPU**. The increment is **1.02 CPU for 300 sign-ins a minute, about 0.20 core-seconds per sign-in and its session end** (netd 0.51 core of it, PostgreSQL 0.31).
* Arithmetic, valid only if cost stays linear (**EXTRAPOLATION**; not verified above 2 vCPU): per minute, the CPU needed is about `0.29 core per 1,000 sessions at the same activity + 0.0034 core per (sign-in per minute)`. A 4 vCPU machine at 70 percent budget (2.8 core) would carry the 1,735-session floor (0.5 core) plus about 670 sign-ins a minute; at 8 vCPU the same arithmetic gives more than the demand of any hotel the product is aimed at. **What this arithmetic ignores, and the lab did not measure:** whether netd's pass loop, which takes the pass lock for the whole pass (lock waits of up to 8.5 s were logged under u32 at 4,585 sessions), and PostgreSQL's single-writer paths scale across cores. Until a 4 vCPU run is made, "more cores = proportionally more sign-ins" is a hypothesis.
* The lab shows where the CPU goes: the two consumers are netd (`nft`/`tc` work per session) and PostgreSQL (accounting ingest, expiry, enforcement bookkeeping). A bigger machine helps only if both can use extra cores.

## 3. Memory

* Measured resident set of the five Go services at the end of the hour: **about 190 MB at the average, 250 MB at the peaks** (netd up to 108 MB). The rest of the container's 3.1 GB is PostgreSQL (the lab set `shared_buffers` to 1 GB, `effective_cache_size` 3 GB, `work_mem` 5 MB) and page cache. There was no memory pressure at 4 GB.
* Kernel memory per shaped session was measured in the real-kernel test at 194 MB for 6,000 sessions (about 32 KB each, `evidence/.../network19/shape-real.log`); 4,585 sessions in the lab appliance ran inside 4 GB with the same Go and PostgreSQL footprint.
* **EXTRAPOLATION:** on 16 GB the arithmetic says the working set is not memory-bound at the session counts above: services about 0.3 GB, kernel state about 32 KB x sessions (65 MB for 2,000), conntrack about 3,600 entries peak in this run (262,144 is the configured maximum), PostgreSQL shared buffers at the image's 25 percent (4 GB). How much a larger buffer pool helps the accounting tables (the hot set is the last two days of raw samples, section 4) was not measured.

## 4. Storage: rows and bytes per session (measured), growth per day (arithmetic)

Measured per-row sizes (table plus its indexes, from `pg_total_relation_size` over the hour's row counts):

| Table | Rows added in the hour | Size added | Bytes per row |
|---|---|---|---|
| `iam_v2.accounting_records` (raw samples) | 322,008 | 83.2 MB | **258** (248 over the whole table) |
| `iam_v2.accounting_usage_minute` | 114,102 | 19.1 MB | **167** |
| `iam_v2.accounting_usage_hour` | 19,844 | 3.6 MB | **181** |
| `iam_v2.sessions` | 18,001 | 11.1 MB | **617** |
| `iam_v2.entitlements` | 18,001 | 27.1 MB | **1,505** |
| `iam_v2.auth_contexts` | 18,001 | 7.3 MB | **405** |
| `iam_v2.accounting_checkpoints` | 18,001 | 11.3 MB | **628** |

* **One sign-in leaves about 3.2 KB of durable rows** (sessions + entitlement + auth context + checkpoint), plus the voucher row it redeemed. These are not purged by the accounting retention.
* **Raw samples: one row about every 16 s (median; per-session range 121 to 231 rows an hour) for a session that moves bytes, none for a session that does not** (idle, 5 minutes with 3,885 live sessions and no traffic: 0 new raw or minute rows). The measured mix (each long session moving about every 50 s) gave 178 rows an hour per long session and 3.0 rows per 60-second session. The upper bound for a session that always moves is about 231 rows an hour, **5,544 a day, 1.4 MB a day of raw table and index per always-active session**.
* Minute rows: 114,102 for 1,700 long sessions over 60 minutes plus 18,001 short ones, i.e. about one per active session per minute, **167 B x 1,440 = 0.24 MB a day per always-active session**. Hour rows: **181 B x 24 = 4.3 KB a day**.
* Retention defaults that bound the big tables (migration 0175, `iam_v2.site_accounting_retention`): raw samples 2 days, minute rows 7 days, both purged by acctd every 10 minutes within a 10 s budget; hour rows and the per-entitlement accumulators follow the site's usage-detail setting, **default unlimited**.
* Database growth measured in the hour: **+227.6 MB** (365.9 to 593.5 MB; 164 MB of it in the seven tables above). This is the extreme churn case.
* **WAL (measured): 2.615 GB in the hour (9.47 million records, 283,233 full-page images), 1.84 GB in the identical first run; 43 MB an hour at rest** (3,885 live sessions, no traffic, 5-minute sample). WAL is recycled, so it is a rate that PostgreSQL and any archive or replica see, not disk that accumulates; the disk it needs is `max_wal_size` plus `wal_keep_size` plus whatever an archive holds. **EXTRAPOLATION:** sustained at the measured rate that is 44 to 63 GB of WAL a day, which no hotel produces; the rate at a realistic sign-in rate was not measured.

**Worked example (EXTRAPOLATION: assumes 2,000 sessions always active, 4,000 sign-ins a day, the lab's row sizes, default retention):**

| Item | Per day | Resident |
|---|---|---|
| Raw samples | 2,000 x 5,544 x 258 B = 2.9 GB | 2 days = 5.7 GB |
| Minute rows | 2,000 x 1,440 x 167 B = 0.48 GB | 7 days = 3.4 GB |
| Hour rows | 2,000 x 24 x 181 B = 8.7 MB | 3.2 GB a year (until the site sets a retention) |
| Sign-in rows | 4,000 x 3.2 KB = 12.8 MB | 4.7 GB a year |
| **Database** | | **about 9 GB after the first week (5.7 + 3.4 + the small rows), about 17 GB after a year (5.7 + 3.4 + 3.2 + 4.7)** |

Half the sessions always active halves the first three lines. The product's own housekeeping (`deploy/scripts/backup-retention.conf`: `KEEP_DB=7`, `BACKUP_KEEP=7`, `SAFETY_BACKUP_KEEP=3`, `KEEP_BINARIES=5`, `KEEP_RELEASES=5`, `DISK_WARN=80`, `DISK_CRIT=90` percent) keeps up to seven database backups and three safety backups: **the backup size was not measured**, so the disk need is `database + (7 + 3) x backup size + WAL + logs + the operating system, Docker images and releases (not measured; plan for 15 to 20 GB)`. If a backup were as large as the database (uncompressed), the example above needs about 17 + 170 GB after a year, which does **not** fit in 100 to 150 GB; with backups at a quarter of the database it needs about 17 + 43 GB plus the 15 to 20 GB of system, which does. This is the one place where the 100 to 150 GB target is not safe by arithmetic alone, and it should be checked by taking one real backup of a populated database.

## 5. What the lab showed that bears on sizing but is not a size

* Sign-in latency, not throughput, is what degrades first: p99 5.1 s at the reference with CPU at the edge. If the larger machine keeps the same per-sign-in work, the same hotel sees lower latency; this was not measured.
* The accounting purge keeps up: at the lab's raw rate it never fell behind (raw rows 479,736 at the start and 801,744 at the end of the hour with the two-day window not yet reached; the purge itself was not exercised at lab scale in this run).
* An appliance restart returns guests in 1 to 7 minutes depending on how many sessions it holds (netd re-asserts about 12 sessions a second after start); this does not improve with memory and may improve with cores.
* A `/19` client network is address capacity, not a capacity claim: the most sessions on one bridge in the lab were 2,150, and the upper half of a `/19` could not be enforced at all until a database function is corrected (see the `/19` results).

## 6. What is not known and would need a run

1. A 4 or 8 vCPU run of the same hour: whether netd and PostgreSQL scale across cores.
2. The same hour on a real disk with the target `fdatasync` latency (the lab disk gave 2 to 3 ms in earlier rounds).
3. 16 GB with PostgreSQL at 4 GB of shared buffers.
4. One real backup and one restore of a populated database, for the backup multiplier above.
5. Table sizes after 24 hours and after the first purge at realistic (not stress) sign-in rates.
6. More than 2,400 simulated clients: the generator, not the appliance, was the limit at 2,400.
