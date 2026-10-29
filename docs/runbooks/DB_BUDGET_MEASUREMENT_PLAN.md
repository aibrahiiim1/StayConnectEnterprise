# Database, pool and memory budget: measurement plan (audit AD-50, option b)

**Status: PARTLY MEASURED (2026-10-29), nothing needed changing.** The Product Owner decided to measure first. A first measurement was taken on the test appliance (1 vCPU, 8 GiB; section "Measured" below). It corrects two assumptions this plan was written with, and found no setting near a limit, so no value was written. The full run of the plan (2,000 generated devices, one hour; S1, S10) on a 2 vCPU / 4 GB reference VM is still to do. Nothing here changes a running appliance.

## What ships now

* `log_parameter_max_length=0` and `log_parameter_max_length_on_error=0` on the appliance and Central PostgreSQL (`deploy/compose/infra.yml`, `central-infra.yml`): no bound parameter value (a name, a room, an address) can reach the database log, whatever logging is switched on later.
* **Per-role timeouts already exist** (Gate-P, `deploy/gatep/gatep-roles.sql`): `statement_timeout`, `lock_timeout` and `idle_in_transaction_session_timeout` for scd, edged, acctd, netd and pmsd, and 30 s statement timeouts on the payment and posting roles. They are per role on purpose: migrations, backups and restores run as other roles and are not limited.
* **Not shipped, because nothing measured needs it:** a global `statement_timeout` (it would break a long migration, backup or restore), changed pool sizes, `shared_buffers` / `work_mem` / `max_connections`, systemd `MemoryMax` (an OOM kill of netd would cut enforcement).

## Design targets (kept)

2,000 devices; 300 sign-ins a minute; 2 vCPU / 4 GB minimum, 8 GB for stress load. Per-source rate limits are not client capacity: 300-500 clients connecting at once must be served.

## The reference VM

2 vCPU, 4 GB, 80 GB disk (the AD-48 reference), the appliance schema from the current baseline plus every migration, the generator at design load (2,000 devices, 10% of them moving counters each pass, 300 sign-ins a minute for an hour).

## What to run and what to record

| Run | Measure | Writes this value |
|---|---|---|
| S1 steady state, 1 hour | Postgres RSS, `pg_stat_database` (xact_commit, blks_hit ratio), `pg_stat_activity` connections by role, slow statements (temporary `log_min_duration_statement=250ms`, with the two parameter settings above) | `shared_buffers`, `work_mem`, per-service pool size |
| S2 sign-in burst, 500 connecting at once | per-role connection high-water mark, pool wait time, error rate | per-role `CONNECTION LIMIT` (the roles now add up to 85 against 50 allowed), pool sizes |
| S10 disk and purge | table sizes after 24 h; `iam_v2.accounting_records` insert rate; time one `iam_v2.purge_accounting_detail` run takes at that rate | `purge_budget_seconds`, `raw_retention_days` defaults (migration 0175); whether `accounting_records` must become a hypertable |
| Longest legitimate statement / transaction per role | `max(now()-xact_start)` sampled every second, migration and backup windows included | `statement_timeout` and `idle_in_transaction_session_timeout` per role (never global) |
| Memory by service | RSS of every systemd unit at S1 and S2 | `MemoryMax` and OOM kill order |

Record the numbers in the change that writes the values, with the VM size and the date.

## Measured, test appliance, 2026-10-29

1 vCPU, 7.9 GiB, PostgreSQL 16 / TimescaleDB 2.16.1 in a container, appliance release `onegate-appliance-1cd37145c468`, no guests on the network.

| Observation | Value |
|---|---|
| `max_connections` | 100 (the default; the plan assumed 50). Per-role `CONNECTION LIMIT`s add to 85: scd 20, edged 20, acctd 10, netd 10, pmsd 10, payment 5, payment outcome 5, posting 5 |
| `shared_buffers` / `work_mem` | 2.5 GiB (the image sizes it at 25% of RAM) / 10 MiB |
| Burst: 500 then 300 simultaneous sign-in attempts to scd (`scripts/signin-burst-test.sh`) | all answered, 0 busy, 0 timeouts; p50 25 / 21 ms, p95 66 / 57 ms, max 510 / 107 ms. The attempts used synthetic devices that do not exist, so they refuse at the credential step: they exercise the request path, the throttle and the database, not a granted session |
| Database connections during the bursts | at most 15 in all; scd 7 (limit 20); no session held a transaction open longer than the 0.4 s sampling step |
| Resident memory before / after | scd 26 / 27 MiB, portald 24 / 24, acctd 13 / 14, edged 156 / 25 (garbage collection), netd 81 / 18 (it was restarted on its hardened unit in between); PostgreSQL backends about 0.7 GiB in total with shared buffers counted repeatedly |
| Load average during the second burst | 2.2 on one core |
| Console API through Caddy and Node, against straight to edged (AD-59) | 8.2 ms against 2.9 ms (`/central/status`) and 9.8 against 4.4 ms (`/vouchers?limit=5`) at one caller; about 110 requests a second against 270 at sixteen callers, the load generator sharing the core |

## Measured, S10 accounting load, test appliance, 2026-10-29

A scratch database (a schema-only copy of `iam_v2`, dropped afterwards; the live data was not touched) on the same 1 vCPU appliance, loaded with 2,000 sessions and 3,000,000 samples spread over 3 days. This is the database half of the design load; it is not 2,000 real devices on the network.

| Observation | Value |
|---|---|
| Design arrival rate | about 67 samples a second, 5.8 million a day (2,000 devices, 10 % moving counters per pass) |
| Bulk insert | 3,000,000 rows in 45 s; 733 MB with its three indexes |
| Roll-up read | 2.17 million samples in 26 s (about 85,000 a second) once the minute and hour rows existed; the first pass that creates them ran about 25,000 a second. 3,000,000 samples became 3,000,000 minute rows (one sample per device per minute in this data) and 146,000 hour rows |
| Purge delete rate | about 9,700 raw samples a second: one run (budget 10 s, batch 5,000) removed 95,000 to 100,000 rows and stopped on its budget |
| Does the purge keep up | acctd runs it every 10 minutes, up to 20 calls of the function each; at the measured rate one run removes at least 97,000 rows, so 144 runs a day remove at least 14 million against 5.8 million arriving: about 2.4 times the arrival rate on one core, with the work spread so no run holds a long transaction |
| Found: roll-up backlog and the 30 s timeout | acctd's database role cancels a statement after 30 s and acctd called the roll-up with the function's one-day default window. A backlog of 2.17 million samples took 26 s, so a roll-up that had stopped for about 10 hours at design load could not finish inside the timeout and would have failed every minute. acctd now reads in two-hour windows (about 480,000 samples, 6 to 12 s here) and repeats until a window is empty, so a stop of any length is caught up in steps |

Still not measured: real devices moving counters through the whole path, 300 successful sign-ins a minute for an hour, table sizes after 24 hours of real traffic, and the 2 vCPU / 4 GB target machine.
