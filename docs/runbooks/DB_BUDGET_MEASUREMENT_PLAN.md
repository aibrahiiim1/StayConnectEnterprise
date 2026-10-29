# Database, pool and memory budget: measurement plan (audit AD-50, option b)

**Status: EVIDENCE-DEPENDENT.** The values below are not written yet on purpose: the Product Owner decided to measure on a reference VM first and then write them. Nothing here changes a running appliance.

## What ships now

* `log_parameter_max_length=0` and `log_parameter_max_length_on_error=0` on the appliance and Central PostgreSQL (`deploy/compose/infra.yml`, `central-infra.yml`): no bound parameter value (a name, a room, an address) can reach the database log, whatever logging is switched on later.
* **Not shipped, because it cannot be shown safe without the run below:** a global `statement_timeout`, `idle_in_transaction_session_timeout`, pool sizes per service, `shared_buffers` / `work_mem` / `max_connections`, systemd `MemoryMax`. Each can break a long migration, a backup, a restore or a burst of sign-ins if set too low.

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
