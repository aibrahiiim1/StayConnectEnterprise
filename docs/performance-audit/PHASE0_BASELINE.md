# Phase 0: Baseline, targets, method and phase map

**Audited:** master `653363d7`. Code and repository only: nothing was run against PRE-LIVE or Central.

This phase sets what "fast enough" means, how each later phase measures it, and which code each phase covers. It also records what the repository already gives us for measuring, and the first facts found while mapping.

## Summary

| Critical | High | Medium | Low | Clean code |
|---|---|---|---|---|
| 0 | 0 | 1 | 0 | 1 |

Five more items are **handed on** to later phases as Suspected, with the measurement that settles each.

## 1. What the solution is, for performance purposes

One appliance per hotel runs everything a guest touches, on one VM:

| Process | Role on the hot path | Size (non-test Go lines) |
|---|---|---|
| scd | guest sign-in, sessions, vouchers, OTP, PMS validation, background loops | 21,619 |
| portald | the Client Portal every guest device opens | 11,831 |
| edged | the Admin Console API | 32,348 |
| netd | nftables, tc shaping, Kea, system network | 7,590 |
| acctd | traffic accounting | 1,060 |
| pmsd | PMS feed and roster | 174 (most of it in `internal/pmsd`, `internal/pms`) |
| hotel-admin | the Admin Console web app (Next.js 15, React 18), served by Node on the appliance | |
| Postgres 16 + TimescaleDB 2.16.1 | in Docker (`deploy/compose/infra.yml:13`) | 209 tables, 114 indexes in the baseline schema |
| Redis 7 | in the same compose file | |
| Caddy, Kea, Unbound | front proxy, DHCP, DNS | |

Central (`ctrlapi`, `cloud-admin`, Postgres, Redis, Caddy) serves every appliance for identity, activation and licensing only (CLAUDE.md §0E).

## 2. Targets: what the audit measures against

The repository states no performance targets. These are the ones this audit uses. They come from what is already sold and deployed, and **the Product Owner can change them** (decision D1 below). A different target changes severities, not findings.

**Reference appliance.** 2 vCPU, 4 GB RAM, 40 GB disk: the documented pilot size (`docs/DEPLOYMENT_APPLIANCE.md` §8, which says it "serves a mid-size site (hundreds of concurrent devices)").

**Reference hotel.**

| Quantity | Design load | Stress load |
|---|---|---|
| Concurrent guest devices | 2,000 (the PRE-LIVE licence, `docs/DEPLOYED_STATE.md:140`) | 5,000 |
| Rooms | 500 | 1,000 |
| In-house reservations in the PMS roster | 600 | 1,500 |
| Sign-ins in one minute (a conference break, a coach arriving) | 300 | 1,000 |
| Captive-portal probes from devices (OS connectivity checks) | 2,000 devices, each every 30–60 s | 5,000 devices |
| Admin Console users at once | 10 | 25 |
| Data kept | the retention settings' defaults; one year of accounting and audit history | three years |

**Reference fleet (Central).** 200 appliances at design load, 1,000 at stress, each calling on its normal schedule.

**Response-time goals at design load**, on the reference appliance:

| Path | p95 goal |
|---|---|
| Client Portal page | 300 ms |
| Guest sign-in, from submit to internet access | 2 s, including enforcement |
| Captive-portal probe answer | 50 ms |
| Admin Console list or report page (API) | 1 s |
| Admin Console export of a month | 30 s, without slowing guest paths |
| Applying a network or firewall change | 10 s |

**Resource goals:** all processes together fit in 4 GB with at least 15% headroom at design load, and no memory, table or file grows without a limit over time.

## 3. Method

Each phase uses whichever of these the area needs, and says which it used:

1. **Code reading** for work that grows with load or data: queries in loops, full-table scans, unbounded result sets, locks held during I/O, per-request work that could be done once, polling loops, and missing indexes for the `WHERE` and `ORDER BY` actually used.
2. **Query plans** (`EXPLAIN (ANALYZE, BUFFERS)`) on a throwaway full-schema database built with `scripts/lib-fullschema-testdb.sh`. The data is generated at the reference sizes above, and never copied from an appliance.
3. **Go benchmarks and profiles** (`go test -bench`, `-cpuprofile`, `-memprofile`) for hot functions. These are written in the throwaway workspace and not committed; the fix batches add the ones worth keeping.
4. **Load runs** against the services in a container limited to the reference size (`--cpus 2 --memory 4g`), with generated data. They run only where a phase can do so without the kernel or network topology of a real appliance. Phase 4 uses the privileged network-namespace container from the backend audit.
5. **Kernel tests** for nftables and tc at the reference device counts, in the same throwaway namespace container.

**Limits of the method.** The workstation runs WSL2 (kernel 6.6), not the appliance's kernel or disk. Numbers from it are used to compare and to find growth curves, not as promises. Phase 11 writes the plan to repeat the important ones on a reference-size VM.

**Cleanup.** Every container and volume is removed with `docker rm -f -v` after each run, and images pulled for the audit are removed at the end of the phase. Nothing is pruned globally.

## 4. Findings

### B0-1: There is nothing to measure performance with (Medium, Confirmed)

**Where:** the whole repository.

**Problem.**
- There is no Go benchmark anywhere: no `func Benchmark` in `data-plane`, `control-plane` or `license`.
- No service exposes a profiler (`net/http/pprof` is not imported anywhere).
- Only scd (`internal/metrics/metrics.go`) and ctrlapi (`control-plane/internal/metrics`) export Prometheus metrics. portald, edged, netd, acctd and pmsd export none, and scd's metrics have only one latency histogram (`PMSValidateDuration`).
- There is no load-test harness or generated-data fixture at hotel size.

**Impact.** A slowdown can be neither seen on PRE-LIVE nor caught before release, and no fix in this audit can show its effect without first building the measurement.

**Recommended fix.**
1. A request-duration histogram, by route pattern (never the raw path), in every HTTP service, using the scd registry's pattern.
2. A profiler endpoint on the local admin socket or loopback only, off by default, switched on by a setting.
3. A `perf` build tag with benchmarks for the hot functions each phase names.
4. A data generator that fills a throwaway full-schema database to the reference sizes in §2.

**Proof.** The benchmarks run in the on-demand check gate (backend audit P0 B0a). A load run produces a p95 per route.

**Batch:** B0 (first, before any other performance batch: every later fix measures with it).

### B0-2: Measurement code is scattered across services (Clean code, Confirmed)

**Where:** `data-plane/internal/metrics` is scd's own registry; `data-plane/internal/reqlog` logs requests; each service builds its own `http.Server`.

**Problem.** There is no shared place to add the request histogram, the profiler switch or the server timeouts (backend audit pattern P1). Each service would get its own copy.

**Recommended fix.** One small shared package for the HTTP server (timeouts, request histogram, request id), used by every service. It is the same helper the backend audit's pattern P1 asks for, so build them together.

**Batch:** B0.

## 5. Handed on to later phases (Suspected)

| # | What was seen | Why it may matter | Phase, measurement |
|---|---|---|---|
| H1 | No appliance service sets a connection-pool size. Every `pgxpool.New` uses the pgx default, `max(4, CPUs)`, which is **4 connections** per pool on the reference appliance. Central sets 10 (`control-plane/internal/db/db.go:15`). | A sign-in storm, a long report and the background loops of one service share 4 connections; requests queue for a connection, not for the database. | Phase 1: pool wait time under the Phase 2 storm. |
| H2 | Guest passwords use Argon2id with 64 MiB of memory per hash (`internal/passwordhash/passwordhash.go:18`), in `scd/self_service_handlers.go` and `iamv2/adapters.go`. Nothing visible limits how many run at once. | 30 at once need about 2 GB, half the reference appliance. | Phase 2: memory and latency with 50 parallel sign-ins. |
| H3 | Postgres runs in Docker with no memory limit, and the TimescaleDB image tunes itself at first start (`deploy/gatep/gatep-dryrun.sh:43`) against the host's memory, not the share left after the other services. Only hotel-admin has a memory cap on the appliance (`MemoryMax=768M`). | Postgres may be tuned as if it owned the box, and the kernel's out-of-memory killer then chooses who dies. | Phase 1 (settings) and Phase 10 (the memory budget). |
| H4 | Only `accounting_records` and `audit_log` are hypertables, and the schema has no TimescaleDB retention or compression policy. Deletion is done by scd's own loops (`data_retention.go`, `log_retention.go`). | Deleting old rows one by one from large tables is slow and leaves bloat; dropping chunks is not. | Phase 1 (growth) and Phase 5 (the retention loops). |
| H5 | No code writes in batches: `pgx.Batch` and `CopyFrom` are not used anywhere on the appliance. There are 43 periodic loops (`time.NewTicker`) across the services, from every 2 s (`posting_worker.go`, `pmsd/pg.go`) to every 6 h. | Round trips per row in accounting and roster loads; idle polling load on a small box. | Phase 5 (loops and accounting) and Phase 6 (roster load). |

## 6. Phase map

| Phase | Covers | Main measurements |
|---|---|---|
| 1 Database foundations | Postgres settings in `deploy/compose/infra.yml` and its tuning; every `pgxpool.New`; table growth per day at design load; hypertables and retention; index use for the queries of phases 2–7; vacuum and bloat; long transactions and lock waits | generated data at 1 and 3 years; `pg_stat_statements`; index-use and bloat queries |
| 2 Guest sign-in | `cmd/scd` sign-in handlers, `internal/iamv2`, `otp`, `throttle`, `signinattempt`, `staygrant`, `enforce`, `passwordhash`, the PMS validation call | sign-in storm of 300 and 1,000 per minute; memory with parallel hashes; lock waits |
| 3 Client Portal | `cmd/portald` page rendering, templates, languages, assets, `portaldesign`; probe answers; Caddy in front | p95 page time and size; probes per second it can answer; caching |
| 4 Enforcement | `cmd/netd`, `internal/nft`, `nftconverge`, `netcfg`, `shape`, `shapeplan`, `shapeproducer`, `conntrack`, `enforce` | time to add and remove a device at 2,000 and 5,000; full ruleset apply time; tc class count; conntrack table size |
| 5 Background jobs and accounting | `cmd/acctd`, every scd loop, `posting`, `postinghandoff`, `poststay`, `checkout`, `ledger`, the retention loops | rows written per minute; loop cost when idle and at load; retention run time on 1 year of data |
| 6 PMS | `cmd/pmsd`, `internal/pmsd`, `pms`, `pmsrest`, `pmsresolve`, `pmsguard` | full roster load and resync at 600 and 1,500 stays; FIAS frames per second; lookup time per sign-in |
| 7 Admin Console API | `cmd/edged` list, search, report, dashboard and export handlers | p95 per route on 1-year data; result sizes; queries per request |
| 8 Admin front ends | `hotel-admin`, `cloud-admin`: bundle sizes, polling intervals (`setInterval`, refetch), large tables, Node memory | build output sizes; requests per minute from an idle console; render time of the largest pages |
| 9 Central | `cmd/ctrlapi`, `control-plane/internal/*`: appliance calls (hello, licence, assignment), the console's fleet pages, Redis use | calls per minute at 200 and 1,000 appliances; p95 per route; pool use |
| 10 Resource budget | all appliance processes together: memory, CPU, goroutines, open files, log volume, disk growth, startup time, backup and restore time | resident memory per process at design load; the 4 GB budget; backup duration on 1-year data |
| 11 Load-test plan and final report | end-to-end scenarios, the harness to keep, what to repeat on a reference VM; totals, order of work, decisions | |

## 7. Decisions for the Product Owner

| # | Decision | Recommended |
|---|---|---|
| D1 | Are the targets in §2 the ones to design for? In particular 2,000 concurrent devices and 300 sign-ins a minute on 2 vCPU / 4 GB. | Yes, for now. If larger hotels are sold, add a second reference size rather than raising this one. |
| D2 | Should the minimum appliance size be raised if Phase 10 shows 4 GB has no headroom at design load? | Decide after Phase 10. |

Neither blocks the audit: the phases proceed on the §2 targets.

## 8. Tests run in this phase

None. This phase is reading and mapping only.
